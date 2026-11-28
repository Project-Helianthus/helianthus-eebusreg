package base

import (
	"context"
	"errors"
	"sync"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

// Output is the SemReg feed of one output instance.
type Output struct {
	binding   *eebusruntime.OutputBinding
	enabled   bool
	fallbacks []eebusruntime.OutputPowerLimitFallback
}

// feedState is the mutable part of one feed. It stays off Output because an
// exported value cannot carry a sync type.
type feedState struct {
	reason  Reason
	applied map[string]string
}

var (
	stateMu sync.Mutex
	states  = map[*Output]*feedState{}
)

func (o *Output) withState(fn func(*feedState)) {
	if o == nil {
		return
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	state := states[o]
	if state == nil {
		state = &feedState{applied: map[string]string{}}
		states[o] = state
	}
	fn(state)
}

func (o *Output) dropState() {
	if o == nil {
		return
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	delete(states, o)
}

// Reason is the manager reason of the latest committed cut: each resource's
// announcement or loss, and the summary of published resources and losses.
type Reason struct {
	Resources []ResourceLine
	Published int
	Losses    int
	Omissions []AssetGap
	Note      string
}

// ResourceLine is one resource of the latest committed cut.
type ResourceLine struct {
	ResourceID     string
	Announced      bool
	Losses         []string
	SumWithheld    bool
	SumCoefficient string
	SumExponent    int32
	SumUnit        string
	SumReason      string
	NotIncluded    []AssetGap
	Withdrawn      bool
	Grouping       string
}

// AssetGap is one asset a cut did not include, with the reason.
type AssetGap struct {
	Asset  string
	Reason string
}

// New returns the feed for binding. enabled is this instance's configured
// enablement. fallbacks are copied.
func New(binding *eebusruntime.OutputBinding, enabled bool, fallbacks []eebusruntime.OutputPowerLimitFallback) *Output {
	return &Output{
		binding:   binding,
		enabled:   enabled,
		fallbacks: append([]eebusruntime.OutputPowerLimitFallback(nil), fallbacks...),
	}
}

// Reason returns a copy of the manager reason.
func (o *Output) Reason() Reason {
	if o == nil {
		return Reason{}
	}
	var reason Reason
	o.withState(func(state *feedState) {
		reason = copyReason(state.reason)
	})
	return reason
}

// StartOutput reads SemReg until ctx is cancelled. Each committed cut is
// applied on its own. A lens read failure records a loss and applies nothing
// from that cut. A capture failure returns.
func (o *Output) StartOutput(ctx context.Context, input semdriver.SemanticInput, operations semdriver.ScopedOperations) error {
	if o == nil {
		return errors.New("output is unavailable")
	}
	defer o.dropState()
	if o.binding != nil {
		o.binding.RetainSession(operations, o.fallbacks)
	}
	if input == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	cut, err := input.Capture(ctx, semdriver.Filter{})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err := o.applyCut(ctx, input, cut); err != nil {
		return err
	}
	var cursor semdriver.Cursor
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		changes, err := input.Resume(ctx, cursor, semdriver.Filter{})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		cursor = changes.Cursor
		if changes.Resync {
			cut, err = input.Capture(ctx, semdriver.Filter{})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if err := o.applyCut(ctx, input, cut); err != nil {
				return err
			}
			continue
		}
		for _, commit := range changes.Commits {
			if err := o.applyCut(ctx, input, commit); err != nil {
				return err
			}
		}
	}
}

func (o *Output) applyCut(ctx context.Context, input semdriver.SemanticInput, cut semdriver.SemanticCut) error {
	prepared, err := prepareCut(ctx, input, cut.Ref, o.enabled)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o.noteReadFailure(prepared.omissions)
		return nil
	}
	losses := [][]string(nil)
	if o.binding != nil {
		losses = o.binding.ApplyCut(prepared.results)
	}
	o.record(prepared, losses)
	return nil
}

func (o *Output) noteReadFailure(omissions []AssetGap) {
	o.withState(func(state *feedState) {
		state.reason = Reason{
			Published: len(state.applied),
			Losses:    1 + len(omissions),
			Omissions: append([]AssetGap(nil), omissions...),
			Note:      "lens read failed",
		}
	})
}

func (o *Output) record(prepared preparedCut, losses [][]string) {
	o.withState(func(state *feedState) {
		lines := make([]ResourceLine, 0, len(prepared.results))
		for index, result := range prepared.results {
			line := ResourceLine{
				ResourceID: result.ResourceID,
				Grouping:   prepared.grouping[result.ResourceID],
			}
			if index < len(losses) {
				line.Losses = append([]string(nil), losses[index]...)
			}
			if o.binding != nil && o.binding.EntityAddress(result.ResourceID) != "" {
				line.Announced = true
				state.applied[result.ResourceID] = prepared.assets[result.ResourceID]
			}
			if result.MemberSum != nil {
				line.SumWithheld = result.MemberSum.Withheld
				line.SumCoefficient = result.MemberSum.Coefficient
				line.SumExponent = result.MemberSum.Exponent
				line.SumUnit = result.MemberSum.Unit
				line.SumReason = result.MemberSum.Reason
				line.NotIncluded = gapsFrom(result.MemberSum.NotIncluded)
			}
			lines = append(lines, line)
		}
		if !prepared.partial {
			for id, asset := range state.applied {
				if _, present := prepared.seen[id]; present {
					continue
				}
				if omissionHas(prepared.omissions, asset) {
					continue
				}
				lines = append(lines, ResourceLine{ResourceID: id, Withdrawn: true, Losses: []string{"withdrawn"}})
				delete(state.applied, id)
			}
		}
		lossCount := 0
		if prepared.note != "" {
			lossCount++
		}
		lossCount += len(prepared.omissions)
		for _, line := range lines {
			if len(line.Losses) > 0 || line.Withdrawn || line.Grouping != "" || line.SumReason != "" {
				lossCount++
			}
		}
		state.reason = Reason{
			Resources: lines,
			Published: len(state.applied),
			Losses:    lossCount,
			Omissions: append([]AssetGap(nil), prepared.omissions...),
			Note:      prepared.note,
		}
	})
}

func gapsFrom(items []eebusruntime.LensNotIncluded) []AssetGap {
	if len(items) == 0 {
		return nil
	}
	gaps := make([]AssetGap, len(items))
	for index, item := range items {
		gaps[index] = AssetGap{Asset: item.Asset, Reason: item.Reason}
	}
	return gaps
}

func omissionHas(omissions []AssetGap, asset string) bool {
	if asset == "" {
		return false
	}
	for _, omission := range omissions {
		if omission.Asset == asset {
			return true
		}
	}
	return false
}

func copyReason(reason Reason) Reason {
	reason.Resources = append([]ResourceLine(nil), reason.Resources...)
	for index := range reason.Resources {
		reason.Resources[index].Losses = append([]string(nil), reason.Resources[index].Losses...)
		reason.Resources[index].NotIncluded = append([]AssetGap(nil), reason.Resources[index].NotIncluded...)
	}
	reason.Omissions = append([]AssetGap(nil), reason.Omissions...)
	return reason
}

// preparedCut is one captured cut turned into lens results. seen is the set
// of output resources the cut still holds. partial cuts do not withdraw the
// resources they omit.
type preparedCut struct {
	results   []eebusruntime.LensResult
	grouping  map[string]string
	assets    map[string]string
	seen      map[string]struct{}
	omissions []AssetGap
	partial   bool
	note      string
}
