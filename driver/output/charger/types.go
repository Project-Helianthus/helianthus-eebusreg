package charger

// Result is the charger lens input served on one EVSE entity.
type Result struct {
	Family          string
	ResourceID      string
	LensAdmitted    bool
	RuntimeEnabled  bool
	Basis           string
	Elements        []string
	Operations      []Operation
	UseCase         string
	Actor           string
	DeviceActor     string
	MissingElements []string
	Overlapping     bool
	Facts           []struct {
		Element     string
		Phase       string
		Connector   string
		Kind        string
		Coefficient string
		Exponent    int32
		Boolean     bool
		Text        string
		Symbol      string
		Unit        string
	}
	MemberSum *struct {
		Withheld         bool
		Coefficient      string
		Exponent         int32
		Unit             string
		Reason           string
		NotIncludedCount int
		Gaps             []struct {
			Asset  string
			Reason string
		}
	}
}

// Operation is one operation carried on a charger lens result.
type Operation struct {
	Name          string
	Unit          string
	OwnCapability bool
	Converted     bool
}

// Outcome is what a charger lens announces, loses and calls.
type Outcome struct {
	Announced  []string
	Loss       []string
	Operations []string
}

// Placement is the entity the binding stored for the lens resource.
// Binding is false when there is no binding. Found is false when that
// resource has no entity. Node is false when the binding has no node.
type Placement struct {
	Binding bool
	Found   bool
	Kind    string
	Address string
	Node    bool
	Surface any
}
