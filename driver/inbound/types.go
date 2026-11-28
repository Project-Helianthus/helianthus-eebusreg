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

// Remote is one paired device and the entities it publishes.
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
	// Commands names CEM use cases this trusted remote offers:
	// overload_protection, self_consumption, coordinated_charging.
	Commands []string
	// Command is called only when a registered operation is invoked.
	// kind is semreg or namespace. A nil Command sends nothing.
	Command func(kind, operation, feature, entity, value string) error
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
// kelvin; unit K is already kelvin. Phase is the electrical phase name when
// the observation is per phase, otherwise empty.
type Reading struct {
	Field        string
	Phase        string
	Rank         string
	Generation   uint64
	Carrier      string
	Text         string
	Unit         string
	Writable     bool
	ObservedUnix int64
	Feature      string
}

// Result is one publication of the current graph.
type Result struct {
	Nodes   []NodeView
	Links   []LinkView
	Facts   []FactView
	Members []MemberView
	Serials []driver.ManufacturerSerialClaim
	Matches []driver.MatchClaim
	Routes  []driver.SettingRoute
	Gaps    []string
	// LastNotified lists facts whose carrier is notify.
	// A reply-only publication leaves it empty.
	LastNotified []FactView
}

// NodeView is one published node. The key is the durable identity.
// State is current or retired.
type NodeView struct {
	Key   driver.NodeKey
	ID    semreg.ResourceID
	State string
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
	Text            string
	Flagged         bool
	Flag            bool
}

// MemberView is one namespace member and the text it reads.
type MemberView struct {
	Member driver.Member
	Value  string
}
