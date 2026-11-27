package peer

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
	"github.com/Project-Helianthus/helianthus-spine-go/util"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmocks "github.com/Project-Helianthus/helianthus-ship-go/mocks"
)

const (
	opevKey     = "alpha"
	opevPeerDev = "d:_n:opev-peer"
	opevLocal   = "d:_n:opev-local"
)

type opevLink struct {
	mu     sync.Mutex
	queue  chan []byte
	done   chan struct{}
	writes []model.LoadControlLimitDataType
	writer shipapi.ShipConnectionDataWriterInterface
}

func newOPEVLink(t *testing.T) *opevLink {
	t.Helper()
	l := &opevLink{queue: make(chan []byte, 1024), done: make(chan struct{})}
	t.Cleanup(func() { close(l.done) })
	writer := shipmocks.NewShipConnectionDataWriterInterface(t)
	writer.EXPECT().WriteShipMessageWithPayload("mock.Anything").Run(func(message []byte) {
		select {
		case l.queue <- message:
		case <-l.done:
		}
	}).Return().Maybe()
	l.writer = writer
	return l
}

func (l *opevLink) deliver(to shipapi.ShipConnectionDataReaderInterface, observe bool) {
	go func() {
		for {
			select {
			case message := <-l.queue:
				if observe {
					l.observe(message)
				}
				to.HandleShipPayloadMessage(message)
			case <-l.done:
				return
			}
		}
	}()
}

func (l *opevLink) observe(message []byte) {
	var datagram model.Datagram
	if json.Unmarshal(message, &datagram) != nil || datagram.Datagram.Header.CmdClassifier == nil {
		return
	}
	for _, cmd := range datagram.Datagram.Payload.Cmd {
		if *datagram.Datagram.Header.CmdClassifier == model.CmdClassifierTypeWrite && cmd.LoadControlLimitListData != nil {
			l.mu.Lock()
			l.writes = append(l.writes, cmd.LoadControlLimitListData.LoadControlLimitData...)
			l.mu.Unlock()
		}
	}
}

func (l *opevLink) Recorded() []model.LoadControlLimitDataType {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]model.LoadControlLimitDataType(nil), l.writes...)
}

type recorder interface {
	Recorded() []model.LoadControlLimitDataType
}

type opevFixture struct {
	opev   *OPEV
	toPeer recorder
	server *spine.FeatureLocal
	device *spine.DeviceLocal
	remote spineapi.EntityRemoteInterface
}

func limitDescription(id, measurement int, category model.LoadControlCategoryType) model.LoadControlLimitDescriptionDataType {
	limit, meas := model.LoadControlLimitIdType(id), model.MeasurementIdType(measurement)
	return model.LoadControlLimitDescriptionDataType{
		LimitId: &limit, LimitType: util.Ptr(model.LoadControlLimitTypeTypeMaxValueLimit), LimitCategory: &category,
		MeasurementId: &meas, Unit: util.Ptr(model.UnitOfMeasurementTypeA), ScopeType: util.Ptr(model.ScopeTypeTypeOverloadProtection),
	}
}

func limitData(id int, changeable, active bool, ampere float64) model.LoadControlLimitDataType {
	limit := model.LoadControlLimitIdType(id)
	return model.LoadControlLimitDataType{LimitId: &limit, IsLimitChangeable: util.Ptr(changeable), IsLimitActive: util.Ptr(active), Value: model.NewScaledNumberType(ampere)}
}

func standardDescriptions() []model.LoadControlLimitDescriptionDataType {
	return []model.LoadControlLimitDescriptionDataType{
		limitDescription(1, 1, model.LoadControlCategoryTypeObligation),
		limitDescription(2, 2, model.LoadControlCategoryTypeObligation),
		limitDescription(3, 1, model.LoadControlCategoryTypeRecommendation),
		limitDescription(4, 3, model.LoadControlCategoryTypeObligation),
	}
}

func newOPEVFixture(t *testing.T, descriptions []model.LoadControlLimitDescriptionDataType, fourthChangeable bool) *opevFixture {
	t.Helper()
	local := spine.NewDeviceLocal("", "", "", "", opevLocal, model.DeviceTypeTypeEnergyManagementSystem, "")
	localEntity := spine.NewEntityLocal(local, model.EntityTypeTypeCEM, []model.AddressEntityType{1}, 0)
	localEntity.AddFeature(spine.NewFeatureLocal(1, localEntity, model.FeatureTypeTypeLoadControl, model.RoleTypeClient))
	localEntity.AddFeature(spine.NewFeatureLocal(2, localEntity, model.FeatureTypeTypeElectricalConnection, model.RoleTypeClient))
	local.AddEntity(localEntity)

	peerDevice := spine.NewDeviceLocal("", "", "", "", opevPeerDev, model.DeviceTypeTypeChargingStation, "")
	peerEntity := spine.NewEntityLocal(peerDevice, model.EntityTypeTypeEVSE, []model.AddressEntityType{1}, 0)
	server := spine.NewFeatureLocal(1, peerEntity, model.FeatureTypeTypeLoadControl, model.RoleTypeServer)
	server.AddFunctionType(model.FunctionTypeLoadControlLimitDescriptionListData, true, false)
	server.AddFunctionType(model.FunctionTypeLoadControlLimitListData, true, true)
	circuit := spine.NewFeatureLocal(2, peerEntity, model.FeatureTypeTypeElectricalConnection, model.RoleTypeServer)
	circuit.AddFunctionType(model.FunctionTypeElectricalConnectionParameterDescriptionListData, true, false)
	peerEntity.AddFeature(server)
	peerEntity.AddFeature(circuit)
	peerDevice.AddEntity(peerEntity)

	server.SetData(model.FunctionTypeLoadControlLimitDescriptionListData, &model.LoadControlLimitDescriptionListDataType{LoadControlLimitDescriptionData: descriptions})
	server.SetData(model.FunctionTypeLoadControlLimitListData, &model.LoadControlLimitListDataType{LoadControlLimitData: []model.LoadControlLimitDataType{
		limitData(1, true, false, 16), limitData(2, true, false, 16), limitData(3, true, false, 6), limitData(4, fourthChangeable, false, 16),
	}})
	phases := []model.ElectricalConnectionPhaseNameType{model.ElectricalConnectionPhaseNameTypeA, model.ElectricalConnectionPhaseNameTypeB, model.ElectricalConnectionPhaseNameTypeC}
	var parameters []model.ElectricalConnectionParameterDescriptionDataType
	for i, phase := range phases {
		phase := phase
		parameters = append(parameters, model.ElectricalConnectionParameterDescriptionDataType{
			ElectricalConnectionId: util.Ptr(model.ElectricalConnectionIdType(0)),
			ParameterId:            util.Ptr(model.ElectricalConnectionParameterIdType(i + 1)),
			MeasurementId:          util.Ptr(model.MeasurementIdType(i + 1)),
			AcMeasuredPhases:       &phase,
		})
	}
	circuit.SetData(model.FunctionTypeElectricalConnectionParameterDescriptionListData, &model.ElectricalConnectionParameterDescriptionListDataType{ElectricalConnectionParameterDescriptionData: parameters})

	toPeer := newOPEVLink(t)
	toLocal := newOPEVLink(t)
	readerOfPeer := local.SetupRemoteDevice("peerski", toPeer.writer)
	readerOfLocal := peerDevice.SetupRemoteDevice("localski", toLocal.writer)
	toPeer.deliver(readerOfLocal, true)
	toLocal.deliver(readerOfPeer, false)

	var remoteEntity spineapi.EntityRemoteInterface
	eventually(t, "the remote entity is discovered", func() bool {
		device := local.RemoteDeviceForSki("peerski")
		if device == nil {
			return false
		}
		remoteEntity = device.Entity([]model.AddressEntityType{1})
		return remoteEntity != nil && device.FeatureByEntityTypeAndRole(remoteEntity, model.FeatureTypeTypeElectricalConnection, model.RoleTypeServer) != nil
	})
	opev, err := newOPEV(opevKey, localEntity, remoteEntity)
	if err != nil {
		t.Fatal(err)
	}
	return &opevFixture{opev: opev, toPeer: toPeer, server: server, device: peerDevice, remote: remoteEntity}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", what)
}

func (f *opevFixture) selector(phase model.ElectricalConnectionPhaseNameType) OPEVSelector {
	device := model.AddressDeviceType(opevPeerDev)
	return OPEVSelector{
		Instance: opevKey, Device: device, Entity: []model.AddressEntityType{1},
		FeatureType: model.FeatureTypeTypeLoadControl, Function: model.FunctionTypeLoadControlLimitListData,
		LimitType: model.LoadControlLimitTypeTypeMaxValueLimit, LimitCategory: model.LoadControlCategoryTypeObligation,
		ScopeType: model.ScopeTypeTypeOverloadProtection, Unit: model.UnitOfMeasurementTypeA, Phase: phase,
	}
}

func (f *opevFixture) bind(t *testing.T) {
	t.Helper()
	if _, err := f.opev.Bind(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the peer holds the binding", func() bool {
		return len(f.device.BindingManager().BindingsOnFeature(*f.server.Address())) == 1
	})
}

func (f *opevFixture) refresh(t *testing.T, ready func() bool) {
	t.Helper()
	if err := f.opev.Request(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the client holds the remote data", ready)
}

func (f *opevFixture) holds(limit int, active bool, ampere float64) func() bool {
	return func() bool {
		got, err := f.opev.Readback(model.LoadControlLimitIdType(limit))
		return err == nil && got.Active != nil && *got.Active == active && got.Value != nil && got.Value.GetValue() == ampere
	}
}

// selectorReady reports whether Select can already see the phase parameters and the
// limit descriptions. Limit data can be stored first; a single Select before those
// two lists arrive is absence, not the selector result.
func (f *opevFixture) selectorReady(sel OPEVSelector, limit, measurement int) func() bool {
	return func() bool {
		got, err := f.opev.Select(sel)
		return err == nil && int(got.LimitID) == limit && int(got.MeasurementID) == measurement
	}
}

func (f *opevFixture) peerData(t *testing.T, limit int) model.LoadControlLimitDataType {
	t.Helper()
	list := f.server.DataCopy(model.FunctionTypeLoadControlLimitListData).(*model.LoadControlLimitListDataType)
	for _, entry := range list.LoadControlLimitData {
		if entry.LimitId != nil && int(*entry.LimitId) == limit {
			return entry
		}
	}
	t.Fatalf("the peer holds no limit %d", limit)
	return model.LoadControlLimitDataType{}
}

func (f *opevFixture) ack(t *testing.T, receipt OPEVReceipt) {
	t.Helper()
	var result model.ResultDataType
	eventually(t, "the peer returned a result for the write", func() bool {
		var ok bool
		result, ok = f.opev.Result(receipt.Counter)
		return ok
	})
	if result.ErrorNumber != nil && *result.ErrorNumber != 0 {
		t.Fatalf("write %d was refused: %d %s", receipt.Counter, *result.ErrorNumber, *result.Description)
	}
}

func TestPeerOPEVUnboundClientWritesNothing(t *testing.T) {
	f := newOPEVFixture(t, standardDescriptions(), true)
	f.refresh(t, func() bool { _, err := f.opev.Readback(1); return err == nil })
	if _, err := f.opev.Set(f.selector(model.ElectricalConnectionPhaseNameTypeA), 10); !errors.Is(err, errOPEVUnbound) {
		t.Fatalf("set on an unbound client = %v, want %v", err, errOPEVUnbound)
	}
	if _, err := f.opev.Inverse(f.selector(model.ElectricalConnectionPhaseNameTypeA)); !errors.Is(err, errOPEVUnbound) {
		t.Fatalf("inverse on an unbound client = %v, want %v", err, errOPEVUnbound)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.toPeer.Recorded(); len(got) != 0 {
		t.Fatalf("the peer recorded writes from an unbound client: %+v", got)
	}
}

func TestPeerOPEVSetInverseAndReadback(t *testing.T) {
	f := newOPEVFixture(t, standardDescriptions(), true)
	f.bind(t)
	phaseA, phaseB := f.selector(model.ElectricalConnectionPhaseNameTypeA), f.selector(model.ElectricalConnectionPhaseNameTypeB)
	f.refresh(t, func() bool {
		return f.holds(2, false, 16)() && f.selectorReady(phaseA, 1, 1)() && f.selectorReady(phaseB, 2, 2)()
	})

	selection, err := f.opev.Select(phaseA)
	if err != nil || selection.LimitID != 1 || selection.MeasurementID != 1 {
		t.Fatalf("phase a selected %+v, err %v; want obligation limit 1 on measurement 1, not recommendation limit 3", selection, err)
	}
	if other, err := f.opev.Select(phaseB); err != nil || other.LimitID != 2 {
		t.Fatalf("phase b selected %+v, err %v; want limit 2", other, err)
	}

	setA, err := f.opev.Set(phaseA, 10)
	if err != nil {
		t.Fatal(err)
	}
	f.ack(t, setA)
	setB, err := f.opev.Set(phaseB, 8)
	if err != nil {
		t.Fatal(err)
	}
	f.ack(t, setB)
	if setA.Counter == setB.Counter {
		t.Fatal("two writes share one counter")
	}
	f.refresh(t, func() bool { return f.holds(1, true, 10)() && f.holds(2, true, 8)() })
	for limit, want := range map[int]struct {
		active bool
		value  float64
	}{1: {true, 10}, 2: {true, 8}, 3: {false, 6}, 4: {false, 16}} {
		got := f.peerData(t, limit)
		if *got.IsLimitActive != want.active || got.Value.GetValue() != want.value {
			t.Fatalf("peer limit %d = active %v value %v, want %+v", limit, *got.IsLimitActive, got.Value.GetValue(), want)
		}
	}

	inverse, err := f.opev.Inverse(phaseA)
	if err != nil {
		t.Fatal(err)
	}
	if inverse.LimitID != setA.LimitID {
		t.Fatalf("inverse limit %d, set limit %d: the inverse must act on the same limit instance", inverse.LimitID, setA.LimitID)
	}
	f.ack(t, inverse)
	f.refresh(t, func() bool { return f.holds(1, false, 10)() })
	if !f.holds(2, true, 8)() || !f.holds(3, false, 6)() || !f.holds(4, false, 16)() {
		t.Fatal("the unrelated limits changed after the inverse on phase a")
	}

	writes := f.toPeer.Recorded()
	if len(writes) != 3 {
		t.Fatalf("peer recorded %d limit writes, want set a, set b and inverse a: %+v", len(writes), writes)
	}
	if *writes[0].LimitId != 1 || !*writes[0].IsLimitActive || writes[0].Value.GetValue() != 10 ||
		*writes[1].LimitId != 2 || !*writes[1].IsLimitActive || writes[1].Value.GetValue() != 8 ||
		*writes[2].LimitId != 1 || *writes[2].IsLimitActive {
		t.Fatalf("recorded writes do not match set a, set b, inverse a: %+v", writes)
	}
}

func TestPeerOPEVRejectsBeforeAnyWrite(t *testing.T) {
	f := newOPEVFixture(t, standardDescriptions(), false)
	f.bind(t)
	base := f.selector(model.ElectricalConnectionPhaseNameTypeA)
	phaseC := f.selector(model.ElectricalConnectionPhaseNameTypeC)
	f.refresh(t, func() bool {
		if !f.holds(4, false, 16)() || !f.selectorReady(base, 1, 1)() {
			return false
		}
		_, err := f.opev.Select(phaseC)
		return errors.Is(err, errOPEVNotChangeable)
	})

	cases := []struct {
		name   string
		mutate func(*OPEVSelector)
		want   error
	}{
		{"Instance", func(s *OPEVSelector) { s.Instance = "beta" }, errOPEVNoMatch},
		{"Device", func(s *OPEVSelector) { s.Device = "d:_n:other" }, errOPEVNoMatch},
		{"Entity", func(s *OPEVSelector) { s.Entity = []model.AddressEntityType{2} }, errOPEVNoMatch},
		{"FeatureType", func(s *OPEVSelector) { s.FeatureType = model.FeatureTypeTypeMeasurement }, errOPEVNoMatch},
		{"Function", func(s *OPEVSelector) { s.Function = model.FunctionTypeLoadControlLimitDescriptionListData }, errOPEVNoMatch},
		{"LimitType", func(s *OPEVSelector) { s.LimitType = model.LoadControlLimitTypeTypeSignDependentAbsValueLimit }, errOPEVNotObligation},
		{"LimitCategory", func(s *OPEVSelector) { s.LimitCategory = model.LoadControlCategoryTypeRecommendation }, errOPEVNotObligation},
		{"ScopeType", func(s *OPEVSelector) { s.ScopeType = model.ScopeTypeTypeSelfConsumption }, errOPEVNotObligation},
		{"Unit", func(s *OPEVSelector) { s.Unit = model.UnitOfMeasurementTypeW }, errOPEVNotObligation},
		{"Phase", func(s *OPEVSelector) { s.Phase = model.ElectricalConnectionPhaseNameTypeAbc }, errOPEVAbsent},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			sel := base
			sel.Entity = append([]model.AddressEntityType(nil), base.Entity...)
			c.mutate(&sel)
			if _, err := f.opev.Set(sel, 10); !errors.Is(err, c.want) {
				t.Fatalf("set err = %v, want %v", err, c.want)
			}
			if _, err := f.opev.Inverse(sel); !errors.Is(err, c.want) {
				t.Fatalf("inverse err = %v, want %v", err, c.want)
			}
		})
	}

	t.Run("ChangeableFalse", func(t *testing.T) {
		if _, err := f.opev.Set(phaseC, 10); !errors.Is(err, errOPEVNotChangeable) {
			t.Fatalf("set err = %v, want %v", err, errOPEVNotChangeable)
		}
		if _, err := f.opev.Inverse(phaseC); !errors.Is(err, errOPEVNotChangeable) {
			t.Fatalf("inverse err = %v, want %v", err, errOPEVNotChangeable)
		}
	})

	time.Sleep(50 * time.Millisecond)
	if got := f.toPeer.Recorded(); len(got) != 0 {
		t.Fatalf("the peer recorded writes for rejected selectors: %+v", got)
	}
	if peer := f.peerData(t, 4); *peer.IsLimitActive || peer.Value.GetValue() != 16 {
		t.Fatalf("a rejected limit changed on the peer: %+v", peer)
	}
}

func TestPeerOPEVAbsentAndAmbiguousDescriptions(t *testing.T) {
	t.Run("ambiguous", func(t *testing.T) {
		descriptions := append(standardDescriptions(), limitDescription(5, 1, model.LoadControlCategoryTypeObligation))
		f := newOPEVFixture(t, descriptions, true)
		f.bind(t)
		sel := f.selector(model.ElectricalConnectionPhaseNameTypeA)
		f.refresh(t, func() bool {
			if _, err := f.opev.Readback(4); err != nil {
				return false
			}
			_, err := f.opev.Select(sel)
			return errors.Is(err, errOPEVAmbiguous)
		})
		if _, err := f.opev.Set(sel, 10); !errors.Is(err, errOPEVAmbiguous) {
			t.Fatalf("set err = %v, want %v", err, errOPEVAmbiguous)
		}
		if got := f.toPeer.Recorded(); len(got) != 0 {
			t.Fatalf("the peer recorded writes for an ambiguous selector: %+v", got)
		}
	})
	t.Run("recommendation-only", func(t *testing.T) {
		f := newOPEVFixture(t, []model.LoadControlLimitDescriptionDataType{limitDescription(3, 1, model.LoadControlCategoryTypeRecommendation)}, true)
		f.bind(t)
		f.refresh(t, func() bool { _, err := f.opev.Readback(3); return err == nil })
		if _, err := f.opev.Set(f.selector(model.ElectricalConnectionPhaseNameTypeA), 10); !errors.Is(err, errOPEVAbsent) {
			t.Fatalf("set err = %v, want %v", err, errOPEVAbsent)
		}
		if got := f.toPeer.Recorded(); len(got) != 0 {
			t.Fatalf("the peer recorded writes for a recommendation: %+v", got)
		}
	})
}
