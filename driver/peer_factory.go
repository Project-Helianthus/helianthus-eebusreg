// Package driver is the public eeBUS peer factory. A host registers one factory
// and plans one instance per configured key. Each instance keeps its own runtime.
package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/catalog"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

// Kind is the driver kind of every instance this factory plans.
const Kind semdriver.DriverKind = "eebus-inbound"

// Factory plans one eeBUS instance per configured key. Each instance keeps its
// own runtime. Create a factory with NewFactory.
type Factory struct {
	gate    chan struct{}
	planned map[semdriver.InstanceKey]eebusruntime.Config
}

// NewFactory returns the eeBUS driver factory a host registers.
func NewFactory() *Factory {
	return &Factory{gate: make(chan struct{}, 1)}
}

// Descriptor returns the eeBUS inbound family and the driver kind.
func (f *Factory) Descriptor() semdriver.Descriptor {
	return semdriver.Descriptor{
		Families: []catalog.Family{inboundFamily()},
		Kinds:    []semdriver.DriverKind{Kind},
	}
}

// Plan decodes a recognized runtime configuration and returns its instance plan.
// It does not dial or open the runtime. An unrecognized configuration is left
// unplanned and is not an error.
func (f *Factory) Plan(config semdriver.Config) ([]semdriver.InstancePlan, error) {
	key := semdriver.InstanceKey(config.Instance)
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if !runtimeOptionsRecognized(config.Options) {
		return nil, nil
	}
	decoded, err := decodeRuntimeConfig(config.Options)
	if err != nil {
		return nil, err
	}
	if config.Enabled != decoded.Enabled {
		return nil, errors.New("instance enabled flag does not match runtime configuration")
	}
	if _, err := eebusruntime.New(decoded); err != nil {
		return nil, err
	}
	f.lock()
	if f.planned == nil {
		f.planned = map[semdriver.InstanceKey]eebusruntime.Config{}
	}
	f.planned[key] = detachRuntimeConfig(decoded)
	f.unlock()
	return []semdriver.InstancePlan{{Driver: Kind, Instance: key}}, nil
}

// Construct builds the instance this factory planned for the plan's key.
func (f *Factory) Construct(_ context.Context, plan semdriver.InstancePlan) (semdriver.Instance, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.Driver != Kind {
		return nil, errors.New("plan is for another driver")
	}
	f.lock()
	config, ok := f.planned[plan.Instance]
	f.unlock()
	if !ok {
		return nil, errors.New("instance was not planned by this factory")
	}
	return New(plan.Instance, config)
}

// New validates key and config and returns one stopped instance. It does not
// dial or open the runtime.
func New(key semdriver.InstanceKey, config eebusruntime.Config) (semdriver.Instance, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	runtime, err := eebusruntime.New(config)
	if err != nil {
		return nil, err
	}
	return &peerInstance{key: key, runtime: runtime, config: detachRuntimeConfig(config)}, nil
}

func (f *Factory) lock() { f.gate <- struct{}{} }

func (f *Factory) unlock() { <-f.gate }

type peerInstance struct {
	mu      sync.Mutex
	key     semdriver.InstanceKey
	runtime eebusruntime.Runtime
	config  eebusruntime.Config
	session semdriver.Session
	started bool
	fenced  bool
}

func (instance *peerInstance) Start(ctx context.Context, session semdriver.Session) error {
	instance.mu.Lock()
	if instance.fenced {
		instance.mu.Unlock()
		return errors.New("instance withdrawn")
	}
	if instance.started {
		instance.mu.Unlock()
		return errors.New("instance already started")
	}
	instance.mu.Unlock()
	if err := instance.runtime.Start(ctx); err != nil {
		return err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.fenced {
		_ = instance.runtime.Shutdown()
		return errors.New("instance withdrawn")
	}
	if instance.started {
		return errors.New("instance already started")
	}
	instance.session = session
	instance.started = true
	return nil
}

func (instance *peerInstance) FenceWithdrawal() {
	instance.mu.Lock()
	instance.fenced = true
	instance.mu.Unlock()
}

func (instance *peerInstance) Stop(context.Context) semdriver.CloseReport {
	instance.mu.Lock()
	instance.started = false
	instance.session = nil
	runtime := instance.runtime
	instance.mu.Unlock()
	err := runtime.Shutdown()
	return semdriver.CloseReport{Confirmed: err == nil, Err: err}
}

func inboundFamily() catalog.Family {
	return catalog.Family{
		FamilyID:    "eebus-inbound",
		Name:        "eeBUS inbound",
		NativeOwner: "Project-Helianthus/helianthus-eebusreg",
		State:       semreg.QualificationQualified,
		Composition: catalog.CompositionNotComposed,
		Reason:      catalog.ReasonDeclaredBySpecification,
		Evidence: []catalog.Evidence{{
			Owner:     "EEBUS Initiative",
			Kind:      catalog.EvidenceKindSpecification,
			Locator:   "SHIP 1.0.1; SPINE 1.3.0; Limitation of Power Consumption V1.0.0; Limitation of Power Production V1.0.0; Monitoring of Grid Connection Point V1.0.0; Monitoring of Power Consumption V1.0.0; EVSE Commissioning and Configuration V1.0.1; EV Commissioning and Configuration V1.0.1; EV Charging Electricity Measurement V1.0.1; EV State of Charge V1.0.0 RC1; HVAC monitoring use cases V1.0.0",
			Statement: "These public revisions declare the eeBUS inbound family. The factory binds that declaration when it accepts an instance configuration. No hardware test is recorded.",
		}},
		Verification: catalog.VerificationNotTestedOnHardware,
	}
}

func runtimeOptionsRecognized(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return false
	}
	for key := range fields {
		if _, ok := runtimeOptionKeys[key]; ok {
			return true
		}
	}
	return false
}

func decodeRuntimeConfig(raw json.RawMessage) (eebusruntime.Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config eebusruntime.Config
	if err := decoder.Decode(&config); err != nil {
		return eebusruntime.Config{}, errors.New("runtime configuration is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return eebusruntime.Config{}, errors.New("runtime configuration is invalid")
	}
	return config, nil
}

func detachRuntimeConfig(config eebusruntime.Config) eebusruntime.Config {
	if config.Remotes != nil {
		config.Remotes = append([]eebusruntime.Remote(nil), config.Remotes...)
	}
	if config.MutationLabProfiles != nil {
		profiles := make([]eebusraw.MutationLabProfileV1, len(config.MutationLabProfiles))
		for index, profile := range config.MutationLabProfiles {
			profiles[index] = profile.Clone()
		}
		config.MutationLabProfiles = profiles
	}
	if config.OutputPowerLimitFallbacks != nil {
		config.OutputPowerLimitFallbacks = append([]eebusruntime.OutputPowerLimitFallback(nil), config.OutputPowerLimitFallbacks...)
	}
	return config
}

var runtimeOptionKeys = map[string]struct{}{
	"StateRoot":                 {},
	"Interface":                 {},
	"ListenAddress":             {},
	"PairingPolicy":             {},
	"Remotes":                   {},
	"MutationLabProfiles":       {},
	"OutputPowerLimitFallbacks": {},
	"DiscoveryEnabled":          {},
}
