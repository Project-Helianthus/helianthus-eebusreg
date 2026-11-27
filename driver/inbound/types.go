package inbound

import (
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

const (
	// PriorityProducer is the rank of a value the remote itself produces.
	PriorityProducer int32 = 3
	// PrioritySecondary is the rank of a value from a secondary device.
	PrioritySecondary int32 = 2
	// PriorityRelayed is the rank of a relayed copy.
	PriorityRelayed int32 = 1
)

// Scope is the instance a publisher binds. It does not change after New.
type Scope struct {
	Installation string
	Instance     string
	Bridge       string
}

// Remote is one paired heating device and the entities it publishes.
type Remote struct {
	Device        string
	SKI           string
	Manufacturer  string
	Serial        string
	IdentityToken string
	Brand         string
	Vendor        string
	Ready         bool
	Generation    uint64
	Entities      []Entity
	Readings      []Reading
}

// Entity is one native entity of a remote. Address is the native entity
// address. Parent is the containing entity address, empty when the entity
// sits directly on the device.
type Entity struct {
	Type     string
	Address  []uint64
	Parent   []uint64
	Name     string
	Readings []Reading
}

// Reading is one native observation. Rank is producer, secondary, relayed,
// or empty for a value the remote itself produces. Carrier is reply or
// notify and is not part of the source path. Unit degC is converted to
// kelvin; unit K is already kelvin.
type Reading struct {
	Field        string
	Rank         string
	Generation   uint64
	Carrier      string
	Text         string
	Unit         string
	Writable     bool
	ObservedUnix int64
	Feature      string
}

// Result is one publication of the current heating graph.
type Result struct {
	Nodes   []NodeView
	Links   []LinkView
	Facts   []FactView
	Members []MemberView
	Serials []driver.ManufacturerSerialClaim
	Matches []driver.MatchClaim
	Routes  []driver.SettingRoute
	Gaps    []string
}

// NodeView is one published node. The key is the durable identity.
type NodeView struct {
	Key driver.NodeKey
	ID  semreg.ResourceID
}

// LinkView is one published relation.
type LinkView struct {
	Key      driver.LinkKey
	FromSlot string
	ToSlot   string
}

// FactView is one published source of one element.
type FactView struct {
	SubjectSlot     string
	SubjectKind     string
	FactID          string
	Coefficient     string
	Exponent        int32
	Unit            string
	SymbolNamespace string
	SymbolToken     string
	SymbolKnown     bool
	Native          string
	NativeUnit      string
	Availability    string
	Freshness       string
	Priority        int32
	Authoritative   bool
	Path            string
	Carrier         string
	Withdrawn       bool
	Generation      uint64
	ObservedUnix    int64
}

// MemberView is one namespace member and the text it reads.
type MemberView struct {
	Member driver.Member
	Value  string
}
