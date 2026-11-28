package eebusservicebridge

import (
	"context"
	"errors"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

// PairingSessionSurface carries the host session methods whose names the
// public driver package must not declare. The bound registry is what the
// host receives from the registry call.
type PairingSessionSurface struct {
	native semdriver.NativeRegistry
	input  semdriver.SemanticInput
}

// NewPairingSessionSurface binds native as the registry returned to the host.
func NewPairingSessionSurface(native semdriver.NativeRegistry) PairingSessionSurface {
	return PairingSessionSurface{native: native}
}

// WithInput returns a surface whose read is input. A nil input keeps the unused read.
func (surface PairingSessionSurface) WithInput(input semdriver.SemanticInput) PairingSessionSurface {
	surface.input = input
	return surface
}

// Native returns the registry bound by NewPairingSessionSurface.
func (surface PairingSessionSurface) Native() semdriver.NativeRegistry {
	return surface.native
}

// SemanticInput returns a read that reports it is unused. Pairing keeps
// source state on the instance and does not capture through this read.
func (surface PairingSessionSurface) SemanticInput() semdriver.SemanticInput {
	if surface.input != nil {
		return surface.input
	}
	return unusedPairingRead{}
}

type unusedPairingRead struct{}

var errPairingReadUnused = errors.New("not used")

func (unusedPairingRead) Capture(context.Context, semdriver.Filter) (semdriver.SemanticCut, error) {
	return semdriver.SemanticCut{}, errPairingReadUnused
}

func (unusedPairingRead) Resume(context.Context, semdriver.Cursor, semdriver.Filter) (semdriver.Changes, error) {
	return semdriver.Changes{}, errPairingReadUnused
}

func (unusedPairingRead) RetrieveLens(context.Context, graph.CutRef, graph.LensRequest) (graph.LensResult, error) {
	return graph.LensResult{}, errPairingReadUnused
}

func (unusedPairingRead) SumMembers(context.Context, graph.CutRef, graph.LensRequest, semreg.DefinitionRef, semreg.ResourceID) (graph.MemberSum, error) {
	return graph.MemberSum{}, errPairingReadUnused
}
