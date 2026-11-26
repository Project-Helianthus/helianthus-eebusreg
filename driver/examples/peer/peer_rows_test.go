package peer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebus-go/session"
	"github.com/Project-Helianthus/helianthus-spine-go/model"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/roweval"
)

const (
	remoteSKI = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	policyID  = "policy.synthetic"
)

type vectorFile struct {
	Contract string          `json:"contract"`
	Inbound  []inboundVector `json:"inbound"`
	Outbound []outputVector  `json:"outbound"`
}

type inboundVector struct {
	Name         string                                 `json:"name"`
	InstanceKey  string                                 `json:"instance_key"`
	RemoteDevice string                                 `json:"remote_device"`
	Entity       []model.AddressEntityType              `json:"entity"`
	Notified     bool                                   `json:"description_notified"`
	Descriptions []model.MeasurementDescriptionDataType `json:"descriptions"`
	Data         []model.MeasurementDataType            `json:"data"`
	Expect       struct {
		Outcome string `json:"outcome"`
		FactKey string `json:"fact_key"`
		Decimal string `json:"decimal"`
	} `json:"expect"`
	Source string `json:"source"`
}

type outputVector struct {
	Name        string `json:"name"`
	InstanceKey string `json:"instance_key"`
	Input       struct {
		State  string `json:"state"`
		Kelvin string `json:"kelvin"`
	} `json:"input"`
	Expect struct {
		Entry       string `json:"entry"`
		Represented string `json:"represented"`
		Number      *int64 `json:"number"`
		Scale       *int8  `json:"scale"`
		Description struct {
			MeasurementType string `json:"measurementType"`
			ScopeType       string `json:"scopeType"`
			Unit            string `json:"unit"`
		} `json:"description"`
	} `json:"expect"`
	Source string `json:"source"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/peer_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Contract != "peer-vectors/v1" || len(file.Inbound) == 0 || len(file.Outbound) == 0 {
		t.Fatalf("vector file is incomplete: %+v", file.Contract)
	}
	return file
}

type offlineTransport struct{ counter uint64 }

func (o *offlineTransport) SendSubscribe(string, session.Pair) session.SendResult {
	o.counter++
	return session.SendResult{Counter: o.counter, Sent: true}
}

func (o *offlineTransport) SendDelete(string, session.Delete) session.SendResult {
	o.counter++
	return session.SendResult{Counter: o.counter, Sent: true}
}

func (o *offlineTransport) SendNotify(string, model.FeatureAddressType, model.FunctionType, any) error {
	return nil
}

func (o *offlineTransport) PeerHeld(string) ([]session.Pair, bool) { return nil, true }

func featureAddress(device string, entity []model.AddressEntityType, feature uint) model.FeatureAddressType {
	d := model.AddressDeviceType(device)
	f := model.AddressFeatureType(feature)
	return model.FeatureAddressType{Device: &d, Entity: entity, Feature: &f}
}

type fixture struct {
	inst    *Instance
	session *session.Instance
	pair    session.Pair
}

func evidence(seed string) semreg.EvidenceRef {
	return semreg.EvidenceRef{
		Owner: "test.native", Kind: "test.fixture", Contract: "test.native/v1",
		Access: semreg.EvidenceAccessPublic, Redaction: semreg.RedactionNone,
		Digest: semreg.Digest(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(seed)))),
	}
}

func template(key string) semreg.FactCandidate {
	source, epoch, binding := semreg.SourceID("source."+key), semreg.SourceEpochID("epoch."+key), semreg.NativeBindingID("binding."+key)
	generation := semreg.Uint64("2")
	proof := evidence("peer-" + key)
	clock := func(ns string) semreg.TimePoint {
		return semreg.TimePoint{UnixNanoseconds: semreg.Int64(ns), ClockID: "clock.synthetic", UncertaintyNS: "0"}
	}
	monotonic := func(ns string) semreg.MonotonicPoint {
		return semreg.MonotonicPoint{ClockEpochID: "clock:synthetic", Nanoseconds: semreg.Uint64(ns)}
	}
	return semreg.FactCandidate{
		CandidateID: semreg.CandidateID("candidate." + key + ".1"),
		Key:         semreg.FactKey{PackID: "template.key", PackVersion: "9.9.9", FactID: "template.fact", Dimensions: []semreg.Dimension{}},
		Quality: semreg.Quality{
			Assertion: semreg.AssertionObserved, Qualification: semreg.QualificationQualified, Promotion: semreg.PromotionPromoted,
			Validity: semreg.ValidityGood, Availability: semreg.AvailabilityAvailable, Freshness: semreg.FreshnessFresh, Reasons: []semreg.DefinitionID{},
		},
		Times: semreg.Times{
			ReceivedAt: clock("100"), ReceiptMonotonic: monotonic("100"),
			EvaluatedAt: clock("200"), EvaluateMonotonic: monotonic("200"),
		},
		FreshnessPolicy:  semreg.FreshnessPolicy{PolicyID: policyID, Version: "1.0.0", FreshForNS: "1000", RetainForNS: "2000", MaxWallUncertaintyNS: "0"},
		BindingID:        &binding,
		SourceEpochID:    &epoch,
		DriverGeneration: &generation,
		Origin:           semreg.OriginRef{OriginID: semreg.OriginID("origin." + key), Kind: semreg.OriginNativeObservation, SourceID: &source, SourceEpochID: &epoch, BindingID: &binding, Evidence: []semreg.EvidenceRef{proof}},
		Evidence:         []semreg.EvidenceRef{proof},
		Revision:         "1",
	}
}

func catalog() []semreg.DefinitionIndex {
	pack := semreg.PackRef{ID: "helianthus.pack.thermal", Version: "1.0.0"}
	return []semreg.DefinitionIndex{{
		Pack:         pack,
		Fields:       []semreg.DefinitionRef{factRef},
		Services:     []semreg.DefinitionRef{serviceRef},
		Capabilities: []semreg.DefinitionRef{},
		Operations:   []semreg.DefinitionRef{operation},
		EffectRules:  []semreg.DefinitionRef{},
	}}
}

func newFixture(t *testing.T, key, remoteDevice string, entity []model.AddressEntityType) fixture {
	t.Helper()
	localDevice := "d:_n:local-" + key
	inst, err := newInstance(Config{
		Key: key, DeviceAddress: localDevice, StateRoot: t.TempDir(), Catalog: catalog(),
		Subject: semreg.ServiceInstanceID("service.dhw." + key), Template: template(key),
	}, &offlineTransport{})
	if err != nil {
		t.Fatal(err)
	}
	attached := inst.engine.(*core).session
	t.Cleanup(func() { _ = inst.Close() })
	pair := session.Pair{
		Client:            featureAddress(localDevice, []model.AddressEntityType{1}, 5),
		Server:            featureAddress(remoteDevice, entity, 1),
		ServerFeatureType: model.FeatureTypeTypeMeasurement,
	}
	if err := attached.Trust(remoteSKI, true); err != nil {
		t.Fatal(err)
	}
	if err := attached.Connect(remoteSKI, true); err != nil {
		t.Fatal(err)
	}
	if err := attached.Select(remoteSKI, []session.Pair{pair}); err != nil {
		t.Fatal(err)
	}
	if err := attached.NoteResult(remoteSKI, 1, true); err != nil {
		t.Fatal(err)
	}
	if len(attached.Established()) != 1 {
		t.Fatal("the in-memory remote pair was not established")
	}
	return fixture{inst: inst, session: attached, pair: pair}
}

func (f fixture) notify(t *testing.T, descriptions []model.MeasurementDescriptionDataType, data []model.MeasurementDataType, describe bool) {
	t.Helper()
	at := time.Unix(1, 0)
	if describe {
		list := &model.MeasurementDescriptionListDataType{MeasurementDescriptionData: descriptions}
		if !f.session.ReceiveNotify(remoteSKI, f.pair, model.FunctionTypeMeasurementDescriptionListData, list, at) {
			t.Fatal("description notify was not accepted")
		}
	}
	list := &model.MeasurementListDataType{MeasurementData: data}
	if !f.session.ReceiveNotify(remoteSKI, f.pair, model.FunctionTypeMeasurementListData, list, at) {
		t.Fatal("data notify was not accepted")
	}
}

func rat(t *testing.T, text string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		t.Fatalf("%q is not a number", text)
	}
	return r
}

func decimalRat(t *testing.T, d semreg.Decimal) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(d.Coefficient + "e" + fmt.Sprint(d.Exponent10))
	if !ok {
		t.Fatalf("decimal %+v is not a number", d)
	}
	return r
}

func scaledRat(t *testing.T, n *model.ScaledNumberType) *big.Rat {
	t.Helper()
	if n == nil || n.Number == nil {
		t.Fatal("scaled number has no number")
	}
	scale := 0
	if n.Scale != nil {
		scale = int(*n.Scale)
	}
	return rat(t, fmt.Sprintf("%de%d", int64(*n.Number), scale))
}

func assertFact(t *testing.T, reading Reading, key string, decimal string, want string) {
	t.Helper()
	if reading.Outcome != OutcomeFact || reading.Candidate == nil {
		t.Fatalf("outcome = %s (%s %s), want a fact", reading.Outcome, reading.Reason, reading.Detail)
	}
	candidate := reading.Candidate
	if candidate.Key.FactID != "thermal.measurement.dhw_temperature" || candidate.Key.FactID != semreg.DefinitionID(key) {
		t.Fatalf("fact key = %q, want %q", candidate.Key.FactID, key)
	}
	if candidate.CandidateID != semreg.CandidateID(want) {
		t.Fatalf("candidate id = %q, want the instance's own template %q", candidate.CandidateID, want)
	}
	if candidate.Value == nil || candidate.Value.Quantity == nil || candidate.Value.Quantity.Unit != "unit.kelvin" {
		t.Fatalf("value = %+v, want a kelvin quantity", candidate.Value)
	}
	if got := decimalRat(t, candidate.Value.Quantity.Number); got.Cmp(rat(t, decimal)) != 0 {
		t.Fatalf("value = %s K, want %s K", got.FloatString(4), decimal)
	}
	if len(candidate.Key.Dimensions) != 1 || candidate.Key.Dimensions[0].ID != "thermal.dimension.dhw" {
		t.Fatalf("dimensions = %+v", candidate.Key.Dimensions)
	}
}

func TestPeerInboundRow(t *testing.T) {
	for _, vector := range loadVectors(t).Inbound {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			t.Logf("instance %s remote %s entity %v: %s", vector.InstanceKey, vector.RemoteDevice, vector.Entity, vector.Source)
			f := newFixture(t, vector.InstanceKey, vector.RemoteDevice, vector.Entity)
			f.notify(t, vector.Descriptions, vector.Data, vector.Notified)
			reading, err := f.inst.Read(remoteSKI, f.pair.Server)
			if err != nil {
				t.Fatal(err)
			}
			if string(reading.Outcome) != vector.Expect.Outcome {
				t.Fatalf("outcome = %s (%s %s), want %s", reading.Outcome, reading.Reason, reading.Detail, vector.Expect.Outcome)
			}
			if vector.Expect.Outcome == string(OutcomeFact) {
				assertFact(t, reading, vector.Expect.FactKey, vector.Expect.Decimal, "candidate."+vector.InstanceKey+".1")
				return
			}
			if reading.Candidate != nil {
				t.Fatalf("outcome %s published a fact %+v", reading.Outcome, reading.Candidate.Value)
			}
		})
	}
}

func TestPeerOutputRow(t *testing.T) {
	for _, vector := range loadVectors(t).Outbound {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			t.Logf("instance %s: %s", vector.InstanceKey, vector.Source)
			f := newFixture(t, vector.InstanceKey, "d:_n:synthetic-remote", []model.AddressEntityType{1})
			in := OutputInput{State: OutputState(vector.Input.State)}
			if vector.Input.Kelvin != "" {
				d := decimalFromText(t, vector.Input.Kelvin)
				in.Value = semreg.Value{Kind: semreg.ValueQuantity, Quantity: &semreg.Quantity{Number: d, Unit: "unit.kelvin"}}
			}
			result, err := f.inst.Output(in)
			if err != nil || result.Reason != "" {
				t.Fatalf("output err=%v reason=%s %s", err, result.Reason, result.Detail)
			}
			descriptions, ok := f.inst.engine.(*core).feature.DataCopy(model.FunctionTypeMeasurementDescriptionListData).(*model.MeasurementDescriptionListDataType)
			if !ok || len(descriptions.MeasurementDescriptionData) != 1 {
				t.Fatalf("node description data = %+v", descriptions)
			}
			description := descriptions.MeasurementDescriptionData[0]
			if description.MeasurementType == nil || string(*description.MeasurementType) != vector.Expect.Description.MeasurementType ||
				description.ScopeType == nil || string(*description.ScopeType) != vector.Expect.Description.ScopeType ||
				description.Unit == nil || string(*description.Unit) != vector.Expect.Description.Unit {
				t.Fatalf("description = %+v, want %+v", description, vector.Expect.Description)
			}
			list, ok := f.inst.engine.(*core).feature.DataCopy(model.FunctionTypeMeasurementListData).(*model.MeasurementListDataType)
			if !ok {
				t.Fatalf("node list data = %+v", list)
			}
			switch vector.Expect.Entry {
			case "omitted":
				if len(list.MeasurementData) != 0 {
					t.Fatalf("entries = %+v, want the entry omitted", list.MeasurementData)
				}
			case "error":
				if len(list.MeasurementData) != 1 || list.MeasurementData[0].ValueState == nil ||
					*list.MeasurementData[0].ValueState != model.MeasurementValueStateTypeError || list.MeasurementData[0].Value != nil {
					t.Fatalf("entries = %+v, want one entry with valueState error and no value", list.MeasurementData)
				}
			case "value":
				if len(list.MeasurementData) != 1 {
					t.Fatalf("entries = %+v", list.MeasurementData)
				}
				entry := list.MeasurementData[0]
				if entry.ValueType == nil || *entry.ValueType != model.MeasurementValueTypeTypeValue || entry.ValueState != nil {
					t.Fatalf("entry = %+v, want a plain valueType value entry", entry)
				}
				if got := scaledRat(t, entry.Value); got.Cmp(rat(t, vector.Expect.Represented)) != 0 {
					t.Fatalf("represented = %s, want %s", got.FloatString(4), vector.Expect.Represented)
				}
				if vector.Expect.Number != nil {
					if int64(*entry.Value.Number) != *vector.Expect.Number || entry.Value.Scale == nil || int8(*entry.Value.Scale) != *vector.Expect.Scale {
						t.Fatalf("pair = %d/%v, want %d/%d", *entry.Value.Number, entry.Value.Scale, *vector.Expect.Number, *vector.Expect.Scale)
					}
				}
			default:
				t.Fatalf("unknown expected entry %q", vector.Expect.Entry)
			}
		})
	}
	t.Run("not-kelvin-is-rejected", func(t *testing.T) {
		f := newFixture(t, "alpha", "d:_n:synthetic-remote", []model.AddressEntityType{1})
		d := decimalFromText(t, "47.5")
		result, err := f.inst.Output(OutputInput{State: OutputValue, Value: semreg.Value{Kind: semreg.ValueQuantity, Quantity: &semreg.Quantity{Number: d, Unit: "unit.pascal"}}})
		if err != nil || result.Reason != roweval.ReasonUnit {
			t.Fatalf("err=%v reason=%q, want %q", err, result.Reason, roweval.ReasonUnit)
		}
		if list, ok := f.inst.engine.(*core).feature.DataCopy(model.FunctionTypeMeasurementListData).(*model.MeasurementListDataType); ok && list != nil && len(list.MeasurementData) != 0 {
			t.Fatalf("a rejected value reached the node: %+v", list)
		}
	})
}

func decimalFromText(t *testing.T, text string) semreg.Decimal {
	t.Helper()
	r := rat(t, text)
	scale := 0
	for !r.IsInt() {
		r.Mul(r, big.NewRat(10, 1))
		scale--
	}
	n := r.Num().Int64()
	d, err := decimalOf(n, scale)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPeerTwoInstances(t *testing.T) {
	remote := "d:_n:synthetic-remote"
	entity := []model.AddressEntityType{1}
	alpha := newFixture(t, "alpha", remote, entity)
	beta := newFixture(t, "beta", remote, entity)
	description := []model.MeasurementDescriptionDataType{descriptionEntry(1)}
	alpha.notify(t, description, []model.MeasurementDataType{dataEntry(1, 475, -1)}, true)
	beta.notify(t, description, []model.MeasurementDataType{dataEntry(1, 480, -1)}, true)

	first, err := alpha.inst.Read(remoteSKI, alpha.pair.Server)
	if err != nil {
		t.Fatal(err)
	}
	second, err := beta.inst.Read(remoteSKI, beta.pair.Server)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("instance alpha reads %+v, instance beta reads %+v", first.Candidate.Value.Quantity.Number, second.Candidate.Value.Quantity.Number)
	assertFact(t, first, "thermal.measurement.dhw_temperature", "320.65", "candidate.alpha.1")
	assertFact(t, second, "thermal.measurement.dhw_temperature", "321.15", "candidate.beta.1")

	for _, c := range []struct {
		f      fixture
		kelvin string
		want   string
	}{{alpha, "320.65", "47.5"}, {beta, "321.15", "48"}} {
		d := decimalFromText(t, c.kelvin)
		if _, err := c.f.inst.Output(OutputInput{State: OutputValue, Value: semreg.Value{Kind: semreg.ValueQuantity, Quantity: &semreg.Quantity{Number: d, Unit: "unit.kelvin"}}}); err != nil {
			t.Fatal(err)
		}
	}
	assertNode := func(f fixture, want string) {
		t.Helper()
		list, ok := f.inst.engine.(*core).feature.DataCopy(model.FunctionTypeMeasurementListData).(*model.MeasurementListDataType)
		if !ok || len(list.MeasurementData) != 1 || scaledRat(t, list.MeasurementData[0].Value).Cmp(rat(t, want)) != 0 {
			t.Fatalf("node data = %+v, want %s", list, want)
		}
	}
	assertNode(alpha, "47.5")
	assertNode(beta, "48")
	if alpha.inst.engine.(*core).feature == beta.inst.engine.(*core).feature || alpha.inst.engine.(*core).key == beta.inst.engine.(*core).key {
		t.Fatal("two instances share a node feature or a key")
	}

	if err := alpha.inst.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := alpha.inst.Read(remoteSKI, alpha.pair.Server); err == nil {
		t.Fatal("a stopped instance still reads")
	}
	again, err := beta.inst.Read(remoteSKI, beta.pair.Server)
	if err != nil {
		t.Fatal(err)
	}
	assertFact(t, again, "thermal.measurement.dhw_temperature", "321.15", "candidate.beta.1")
	assertNode(beta, "48")
}

func descriptionEntry(id model.MeasurementIdType) model.MeasurementDescriptionDataType {
	scope, unit := model.ScopeTypeTypeDhwTemperature, model.UnitOfMeasurementTypedegC
	return model.MeasurementDescriptionDataType{MeasurementId: &id, ScopeType: &scope, Unit: &unit}
}

func dataEntry(id model.MeasurementIdType, number model.NumberType, scale model.ScaleType) model.MeasurementDataType {
	valueType := model.MeasurementValueTypeTypeValue
	return model.MeasurementDataType{MeasurementId: &id, ValueType: &valueType, Value: &model.ScaledNumberType{Number: &number, Scale: &scale}}
}

func TestPeerSelectorFields(t *testing.T) {
	f := newFixture(t, "alpha", "d:_n:synthetic-remote", []model.AddressEntityType{1})
	base := f.inst.engine.(*core).selector("d:_n:synthetic-remote", []model.AddressEntityType{1}, model.ScopeTypeTypeDhwTemperature, model.UnitOfMeasurementTypedegC)
	table := f.inst.engine.(*core).table(base)
	evaluate := func(sel PeerSelector) roweval.Evaluation {
		t.Helper()
		ev, err := roweval.Evaluate(table, roweval.Acquired[PeerSelector]{
			Selector: sel, Payload: scaledPayload(dataEntry(1, 475, -1).Value),
			Receipt:     roweval.Receipt{ReceivedAt: f.inst.engine.(*core).template.Times.ReceivedAt, Monotonic: f.inst.engine.(*core).template.Times.ReceiptMonotonic},
			Template:    f.inst.engine.(*core).template,
			Roles:       roweval.Roles{Subjects: map[string]roweval.RoleBinding{subjectRole: {ServiceInstance: f.inst.engine.(*core).subject}}, Catalog: f.inst.engine.(*core).catalog},
			Descriptors: descriptors(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	if ev := evaluate(base); ev.Candidate == nil {
		t.Fatalf("the base selector does not match: %s %s", ev.Reason, ev.Detail)
	}
	varied := map[string]func(*PeerSelector){
		"Instance":    func(s *PeerSelector) { s.Instance = "beta" },
		"Device":      func(s *PeerSelector) { s.Device = "d:_n:other-remote" },
		"Entity":      func(s *PeerSelector) { s.Entity = []model.AddressEntityType{2} },
		"FeatureType": func(s *PeerSelector) { s.FeatureType = model.FeatureTypeTypeSetpoint },
		"Function":    func(s *PeerSelector) { s.Function = model.FunctionTypeMeasurementDescriptionListData },
		"ScopeType":   func(s *PeerSelector) { s.ScopeType = model.ScopeTypeTypeOutsideAirTemperature },
		"Unit":        func(s *PeerSelector) { s.Unit = model.UnitOfMeasurementTypedegF },
		"ValueType":   func(s *PeerSelector) { s.ValueType = model.MeasurementValueTypeTypeAverageValue },
	}
	for name, vary := range varied {
		sel := base
		sel.Entity = append([]model.AddressEntityType(nil), base.Entity...)
		vary(&sel)
		if ev := evaluate(sel); ev.Reason != roweval.ReasonNoMatch {
			t.Errorf("varying %s still matched: %s", name, ev.Reason)
		}
	}
}
