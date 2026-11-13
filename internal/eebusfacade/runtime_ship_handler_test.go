package eebusfacade

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

var shipHandlerFixtureTime = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

func shipHandlerLocalSKI() string { return strings.Repeat("1", 40) }

func shipHandlerRemote(digit string) string { return strings.Repeat(digit, 40) }

type shipHandlerPublications struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (publications *shipHandlerPublications) publish(payload []byte) {
	publications.mu.Lock()
	defer publications.mu.Unlock()
	publications.payloads = append(publications.payloads, append([]byte(nil), payload...))
}

func (publications *shipHandlerPublications) count() int {
	publications.mu.Lock()
	defer publications.mu.Unlock()
	return len(publications.payloads)
}

func (publications *shipHandlerPublications) last(t *testing.T, wantCount int) runtimeSnapshotPayload {
	t.Helper()
	publications.mu.Lock()
	defer publications.mu.Unlock()
	if len(publications.payloads) != wantCount {
		t.Fatalf("publications = %d, want %d", len(publications.payloads), wantCount)
	}
	var decoded runtimeSnapshotPayload
	if err := json.Unmarshal(publications.payloads[wantCount-1], &decoded); err != nil {
		t.Fatalf("decode publication: %v", err)
	}
	return decoded
}

func newShipHandlerFixture(t *testing.T, remotes ...string) (*runtimeServiceHandler, *shipHandlerPublications) {
	t.Helper()
	config := RuntimeConfig{}
	for _, remote := range remotes {
		config.Remotes = append(config.Remotes, RuntimeRemote{SKI: remote})
	}
	handler, err := newRuntimeServiceHandler(config, shipHandlerLocalSKI(), func() time.Time { return shipHandlerFixtureTime })
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	publications := &shipHandlerPublications{}
	handler.setPublisher(publications.publish)
	return handler, publications
}

func shipHandlerNoReport(t *testing.T, handler *runtimeServiceHandler) {
	t.Helper()
	select {
	case err := <-handler.errors:
		t.Fatalf("unexpected handler report: %v", err)
	default:
	}
}

func TestRuntimeShipHandlerConstructionNormalizesIdentities(t *testing.T) {
	remoteA, remoteB := shipHandlerRemote("a"), shipHandlerRemote("b")
	handler, err := newRuntimeServiceHandler(RuntimeConfig{Remotes: []RuntimeRemote{
		{SKI: " " + strings.ToUpper(remoteB) + " "}, {SKI: remoteA},
	}}, " "+strings.ToUpper(shipHandlerRemote("c"))+" ", time.Now)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	if handler.localSKI != shipHandlerRemote("c") || handler.runtimeID != "runtime:"+shipHandlerRemote("c") {
		t.Fatalf("identities = %q %q", handler.localSKI, handler.runtimeID)
	}
	if !reflect.DeepEqual(handler.policyRemotes, []string{remoteA, remoteB}) {
		t.Fatalf("policy remotes = %q", handler.policyRemotes)
	}
	if _, err := newRuntimeServiceHandler(RuntimeConfig{}, "not-a-ski", time.Now); err == nil {
		t.Fatal("invalid local SKI was accepted")
	}
	if _, err := newRuntimeServiceHandler(RuntimeConfig{}, shipHandlerLocalSKI(), nil); err == nil {
		t.Fatal("missing clock was accepted")
	}
}

func TestRuntimeShipHandlerCallbacksPublishGraphChanges(t *testing.T) {
	remote := shipHandlerRemote("a")
	handler, publications := newShipHandlerFixture(t, remote)

	handler.RemoteSKIConnected(nil, " "+strings.ToUpper(remote)+" ")
	snapshot := publications.last(t, 1)
	if snapshot.Status.State != "ready" || len(snapshot.Sessions) != 1 ||
		snapshot.Sessions[0].ID != "session:"+remote+":1" || snapshot.Sessions[0].State != "connected" ||
		len(snapshot.Services) != 1 || !snapshot.Services[0].Visible || snapshot.Services[0].SKI != remote {
		t.Fatalf("connected snapshot = %+v", snapshot)
	}

	handler.ServiceShipIDUpdate(remote, " ship-a ")
	snapshot = publications.last(t, 2)
	if snapshot.Sessions[0].ID != "session:ship-a:1" || snapshot.Services[0].SHIPID == nil || *snapshot.Services[0].SHIPID != "ship-a" {
		t.Fatalf("SHIP ID snapshot = %+v", snapshot)
	}
	handler.ServiceShipIDUpdate(remote, " ")
	handler.ServiceShipIDUpdate(shipHandlerRemote("b"), "ship-b")
	publications.last(t, 2)

	handler.ServicePairingDetailUpdate(remote, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, nil))
	snapshot = publications.last(t, 3)
	if snapshot.Sessions[0].State != "degraded" || snapshot.Status.Degradation == nil ||
		snapshot.Status.Degradation.Reason != "remote-disconnect" {
		t.Fatalf("degraded snapshot = %+v", snapshot)
	}

	entries := []shipapi.RemoteService{{Ski: strings.ToUpper(remote), Name: " Name A ", Brand: "Brand A"}, {Ski: "invalid", Name: "ignored"}}
	handler.VisibleRemoteServicesUpdated(nil, entries)
	snapshot = publications.last(t, 4)
	if snapshot.Services[0].Name == nil || *snapshot.Services[0].Name != "Name A" ||
		snapshot.Services[0].Brand == nil || *snapshot.Services[0].Brand != "Brand A" || len(snapshot.Services) != 1 {
		t.Fatalf("visible service snapshot = %+v", snapshot.Services)
	}
	handler.VisibleRemoteServicesUpdated(nil, entries)
	publications.last(t, 4)
	handler.VisibleRemoteServicesUpdated(nil, nil)
	snapshot = publications.last(t, 5)
	if snapshot.Services[0].Visible {
		t.Fatalf("hidden service still visible: %+v", snapshot.Services)
	}

	handler.RemoteSKIDisconnected(nil, remote)
	snapshot = publications.last(t, 6)
	if snapshot.Sessions[0].State != "disconnected" {
		t.Fatalf("disconnected snapshot = %+v", snapshot.Sessions)
	}

	handler.RemoteSKIConnected(nil, remote)
	snapshot = publications.last(t, 7)
	if snapshot.Sessions[0].ID != "session:"+remote+":2" || snapshot.Sessions[0].State != "connected" ||
		snapshot.Services[0].SHIPID != nil {
		t.Fatalf("reconnected snapshot = %+v", snapshot)
	}
	shipHandlerNoReport(t, handler)
}

func TestRuntimeShipHandlerReportsRejectedObservations(t *testing.T) {
	handler, publications := newShipHandlerFixture(t)
	handler.report(nil)
	shipHandlerNoReport(t, handler)

	handler.RemoteSKIConnected(nil, "not-a-ski")
	handler.RemoteSKIConnected(nil, "also-not-a-ski")
	if len(handler.errors) != 1 {
		t.Fatalf("reports = %d, want one buffered report", len(handler.errors))
	}
	if err := <-handler.errors; err == nil || !strings.Contains(err.Error(), "40 hexadecimal") {
		t.Fatalf("report = %v", err)
	}
	if publications.count() != 0 || len(handler.observations) != 0 {
		t.Fatalf("rejected observation was published or kept: %d %+v", publications.count(), handler.observations)
	}

	serializerErr := errors.New("serializer failed")
	handler.setSnapshotSerializer(func(string, string, []runtimeGraphObservation, time.Time) ([]byte, error) {
		return nil, serializerErr
	})
	handler.RemoteSKIConnected(nil, shipHandlerRemote("a"))
	if err := <-handler.errors; !errors.Is(err, serializerErr) {
		t.Fatalf("serializer report = %v", err)
	}
	if publications.count() != 0 {
		t.Fatal("failed serialization was published")
	}
}

func TestRuntimeShipHandlerHonoursLivenessProjection(t *testing.T) {
	remoteA, remoteB := shipHandlerRemote("a"), shipHandlerRemote("b")
	handler, publications := newShipHandlerFixture(t, remoteA, remoteB)
	handler.projectionLivenessAllowed = func(ski string) bool { return ski == remoteB }

	handler.RemoteSKIConnected(nil, remoteA)
	handler.ServiceShipIDUpdate(remoteA, "ship-a")
	handler.ServicePairingDetailUpdate(remoteA, nil)
	handler.RemoteSKIDisconnected(nil, remoteA)
	if publications.count() != 0 {
		t.Fatalf("denied remote published %d snapshots", publications.count())
	}
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: remoteA}, {Ski: remoteB}})
	snapshot := publications.last(t, 1)
	if len(snapshot.Services) != 1 || snapshot.Services[0].SKI != remoteB {
		t.Fatalf("services = %+v, want only the admitted remote", snapshot.Services)
	}
	if _, ok := handler.observations[remoteA]; ok {
		t.Fatal("denied remote was observed")
	}
	shipHandlerNoReport(t, handler)
}

func TestRuntimeShipHandlerPublicationOrderAndSerializer(t *testing.T) {
	handler, publications := newShipHandlerFixture(t)
	graph := []runtimeGraphObservation{{RuntimeID: handler.runtimeID, LocalSKI: handler.localSKI, RemoteSKI: shipHandlerRemote("a")}}
	var arguments []string
	handler.setSnapshotSerializer(func(runtimeID, localSKI string, graph []runtimeGraphObservation, now time.Time) ([]byte, error) {
		arguments = append(arguments, runtimeID+"|"+localSKI+"|"+now.Format(time.RFC3339))
		return []byte(`{}`), nil
	})
	for _, revision := range []uint64{5, 4, 5, 6} {
		if err := handler.publishRuntimeGraphAtRevision(graph, revision); err != nil {
			t.Fatalf("publish revision %d: %v", revision, err)
		}
	}
	if publications.count() != 3 || handler.publishedRuntimeRevision != 6 {
		t.Fatalf("publications = %d at revision %d, want 3 at 6", publications.count(), handler.publishedRuntimeRevision)
	}
	want := handler.runtimeID + "|" + handler.localSKI + "|2001-02-03T04:05:06Z"
	if len(arguments) != 3 || arguments[0] != want {
		t.Fatalf("serializer arguments = %q, want %q", arguments, want)
	}

	handler.setSnapshotSerializer(nil)
	if err := handler.publishRuntimeGraph(graph); err != nil {
		t.Fatalf("default serializer: %v", err)
	}
	if snapshot := publications.last(t, 4); snapshot.Meta.Contract != "helianthus.eebus.runtime.raw-snapshot.v1" {
		t.Fatalf("default serializer contract = %q", snapshot.Meta.Contract)
	}
	handler.setPublisher(nil)
	if err := handler.publishRuntimeGraph(graph); err != nil || publications.count() != 4 {
		t.Fatalf("publication without publisher = %v count %d", err, publications.count())
	}
}

func TestRuntimeShipHandlerTimestampIsUTC(t *testing.T) {
	handler, _ := newShipHandlerFixture(t)
	handler.now = func() time.Time { return time.Time{} }
	if got := handler.timestamp(); !got.Equal(time.Unix(0, 0)) || got.Location() != time.UTC {
		t.Fatalf("zero clock timestamp = %s", got)
	}
	handler.now = func() time.Time { return shipHandlerFixtureTime.In(time.FixedZone("fixture", 3600)) }
	if got := handler.timestamp(); !got.Equal(shipHandlerFixtureTime) || got.Location() != time.UTC {
		t.Fatalf("zoned clock timestamp = %s", got)
	}
}

func TestRuntimeShipHandlerRemoteServiceHelpers(t *testing.T) {
	observation := runtimeGraphObservation{ServiceName: "keep", ServiceModel: "keep"}
	mergeRuntimeRemoteService(&observation, shipapi.RemoteService{
		Name: " ", Identifier: " id ", Brand: " brand ", Type: " type ", Model: "",
	})
	if observation.ServiceName != "keep" || observation.ServiceIdentifier != "id" || observation.ServiceBrand != "brand" ||
		observation.ServiceType != "type" || observation.ServiceModel != "keep" {
		t.Fatalf("merged service = %+v", observation)
	}
	base := runtimeGraphObservation{Visible: true, ServiceName: "n", ServiceIdentifier: "i", ServiceBrand: "b", ServiceType: "t", ServiceModel: "m"}
	if !runtimeServiceObservationEqual(base, base) {
		t.Fatal("identical service observations differ")
	}
	for name, mutate := range map[string]func(*runtimeGraphObservation){
		"visible":    func(value *runtimeGraphObservation) { value.Visible = false },
		"name":       func(value *runtimeGraphObservation) { value.ServiceName = "x" },
		"identifier": func(value *runtimeGraphObservation) { value.ServiceIdentifier = "x" },
		"brand":      func(value *runtimeGraphObservation) { value.ServiceBrand = "x" },
		"type":       func(value *runtimeGraphObservation) { value.ServiceType = "x" },
		"model":      func(value *runtimeGraphObservation) { value.ServiceModel = "x" },
	} {
		changed := base
		mutate(&changed)
		if runtimeServiceObservationEqual(base, changed) {
			t.Fatalf("service %s change was not detected", name)
		}
	}
	session := runtimeGraphObservation{RemoteSKI: "remote", SessionIndex: 3}
	if got := runtimeSessionIdentity(session); got != "session:remote:3" {
		t.Fatalf("session without SHIP ID = %q", got)
	}
	session.ShipID = "ship"
	if got := runtimeSessionIdentity(session); got != "session:ship:3" {
		t.Fatalf("session with SHIP ID = %q", got)
	}
}

func TestRuntimeShipHandlerDeviceCaptureHelpers(t *testing.T) {
	if devices, err := runtimeDevicesForRemote(nil, shipHandlerRemote("a")); devices != nil || err != nil {
		t.Fatalf("nil service devices = %+v %v", devices, err)
	}
	if devices, err := runtimeDevicesForRemoteDevice(nil, shipHandlerRemote("a")); devices != nil || err != nil {
		t.Fatalf("nil remote devices = %+v %v", devices, err)
	}
	identity, err := runtimeIdentity("device", "ski", "address")
	if err != nil || identity != `device:["ski","address"]` {
		t.Fatalf("identity = %q %v", identity, err)
	}
	if _, err := runtimeIdentity("device", func() {}); err == nil {
		t.Fatal("unencodable identity was accepted")
	}
	featureSet := spinemodel.NetworkManagementFeatureSetType("gateway")
	simple := spinemodel.NetworkManagementFeatureSetType("simple")
	label := spinemodel.LabelType("label")
	if got := runtimeDeviceMetadata(&featureSet, &spinemodel.NetworkManagementDeviceDescriptionDataType{}); !reflect.DeepEqual(got, map[string]string{"network_feature_set": "gateway"}) {
		t.Fatalf("feature-set metadata = %q", got)
	}
	got := runtimeDeviceMetadata(&featureSet, &spinemodel.NetworkManagementDeviceDescriptionDataType{NetworkFeatureSet: &simple, Label: &label})
	if !reflect.DeepEqual(got, map[string]string{"network_feature_set": "simple", "label": "label"}) {
		t.Fatalf("description metadata = %q", got)
	}
	description := spinemodel.DescriptionType("description")
	version := spinemodel.SpecificationVersionType("1.0.0")
	if *cloneRuntimeDescription(&description) != "description" || cloneRuntimeDescription(nil) != nil ||
		*cloneRuntimeSpecificationVersion(&version) != "1.0.0" || cloneRuntimeSpecificationVersion(nil) != nil ||
		*runtimeStringPointer("value") != "value" {
		t.Fatal("capture value helpers changed")
	}
}
