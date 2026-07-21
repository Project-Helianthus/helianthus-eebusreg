//go:build linux || darwin

package eebusfacade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestIssue54CanonicalIdentityUsesProtectedStoreInstance(t *testing.T) {
	stateRoot := filepath.Join(canonicalRuntimeTempDir(t), "state")
	first, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("load protected material: %v", err)
	}
	store := issue54ProtectedStoreInstance(t, stateRoot, first)
	wantToken := issue54ExpectedNodeToken(store)
	if got := issue54MaterialNodeToken(t, first); got != wantToken {
		t.Fatalf("node token = %q, want %q", got, wantToken)
	}

	firstConfiguration := issue54Configuration(t, first)
	issue54AssertCanonicalConfiguration(t, firstConfiguration, wantToken)

	restarted, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("restart protected material: %v", err)
	}
	restartConfiguration := issue54Configuration(t, restarted)
	if got := restartConfiguration.Identifier(); got != firstConfiguration.Identifier() {
		t.Fatalf("SHIP ID changed across restart: first=%q restart=%q", firstConfiguration.Identifier(), got)
	}
	if got := restartConfiguration.MdnsServiceName(); got != firstConfiguration.MdnsServiceName() {
		t.Fatalf("mDNS service name changed across restart: first=%q restart=%q", firstConfiguration.MdnsServiceName(), got)
	}

	replacementCertificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "issue54-replacement")
	if err != nil {
		t.Fatalf("create replacement certificate: %v", err)
	}
	replaced := first
	replaced.certificate = replacementCertificate
	replaced.localSKI = certificateSKI(t, replacementCertificate)
	if replaced.localSKI == first.localSKI {
		t.Fatal("replacement certificate unexpectedly retained the original SKI")
	}
	replacementConfiguration := issue54Configuration(t, replaced)
	if got := replacementConfiguration.Identifier(); got != firstConfiguration.Identifier() {
		t.Fatalf("SHIP ID changed after certificate replacement: first=%q replacement=%q", firstConfiguration.Identifier(), got)
	}
	if got := replacementConfiguration.DeviceSerialNumber(); got != wantToken {
		t.Fatalf("replacement serial number = %q, want store-derived token %q", got, wantToken)
	}
}

func TestIssue55HostKeyRepairPreservesCanonicalIdentityAcrossPersistedReload(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	anchor := &runtimeStrictAnchor{}

	initial := acquireMSP04CRuntimeResources(
		t, stateRoot, filepath.Join(root, "admin-initial"), anchor,
		&fakeRuntimeService{started: make(chan struct{})},
	)
	if got := initial.coordinator.recoveryState(); got != "NO_LOCAL_IDENTITY" {
		t.Fatalf("initial recovery = %q, want NO_LOCAL_IDENTITY", got)
	}
	request := exactRuntimeRepairRequest(initial.coordinator, "recover_unavailable_host_key", msp04cOrdinal(8_100))
	if got := initial.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("initial host-key repair = %q", got)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	before := issue55ReloadPersistedIdentity(t, stateRoot, anchor)

	anchor.loseSigningIdentity()
	recovery := acquireMSP04CRuntimeResources(
		t, stateRoot, filepath.Join(root, "admin-recovery"), anchor,
		&fakeRuntimeService{started: make(chan struct{})},
	)
	if state, reason := recovery.coordinator.recoveryState(), recovery.coordinator.recoveryReason(); state != "NO_LOCAL_IDENTITY" || reason != "HOST_KEY_UNAVAILABLE" {
		t.Fatalf("repair precondition = %s/%s", state, reason)
	}
	request = exactRuntimeRepairRequest(recovery.coordinator, "recover_unavailable_host_key", msp04cOrdinal(8_200))
	if got := recovery.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("replacement host-key repair = %q", got)
	}
	if err := recovery.Close(); err != nil {
		t.Fatal(err)
	}
	after := issue55ReloadPersistedIdentity(t, stateRoot, anchor)

	if after.storeInstance != before.storeInstance {
		t.Fatalf("StoreInstance rotated during host-key repair: before=%x after=%x", before.storeInstance, after.storeInstance)
	}
	if after.localSKI == before.localSKI {
		t.Fatalf("certificate SKI did not change during host-key repair: %s", after.localSKI)
	}
	if after.nodeToken != before.nodeToken {
		t.Fatalf("node token rotated during host-key repair: before=%s after=%s", before.nodeToken, after.nodeToken)
	}
	if after.shipID != before.shipID {
		t.Fatalf("SHIP ID rotated during host-key repair: before=%s after=%s", before.shipID, after.shipID)
	}
}

type issue55PersistedIdentity struct {
	storeInstance [sha256.Size]byte
	localSKI      string
	nodeToken     string
	shipID        string
}

func issue55ReloadPersistedIdentity(t *testing.T, stateRoot string, anchor *runtimeStrictAnchor) issue55PersistedIdentity {
	t.Helper()
	bridge, outcome := eebusstore.OpenAssociationBridge(stateRoot, []eebusstore.KeyProviderBinding{anchor.keyBinding()})
	if bridge == nil {
		t.Fatalf("reopen persisted identity = %q", outcome)
	}
	view, outcome := bridge.ReloadControl(context.Background())
	if outcome != "opened_current" && outcome != "opened_migrated" {
		_ = bridge.Close()
		t.Fatalf("reload persisted identity = %q", outcome)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	signer, err := anchor.Unseal(view.Control.LocalSealedBlob)
	if err != nil {
		t.Fatalf("unseal persisted identity: %v", err)
	}
	nodeToken := canonicalRuntimeNodeToken(view.Control.StoreInstance)
	material := runtimeMaterial{
		certificate: newProtectedTLSCertificate(view.Control.LocalCertificateChainDER, signer),
		localSKI:    hex.EncodeToString(view.Control.LocalSKI),
		nodeToken:   nodeToken,
	}
	configuration := issue54Configuration(t, material)
	return issue55PersistedIdentity{
		storeInstance: view.Control.StoreInstance,
		localSKI:      material.localSKI,
		nodeToken:     nodeToken,
		shipID:        configuration.Identifier(),
	}
}

func TestIssue54AllowlistIsPolicyAndCallbacksCreateEvidence(t *testing.T) {
	remoteSKI := "1111111111111111111111111111111111111111"
	handler, err := newRuntimeServiceHandler(RuntimeConfig{Remotes: []RuntimeRemote{{
		SKI: remoteSKI, Allowlisted: true,
	}}}, "2222222222222222222222222222222222222222", time.Now)
	if err != nil {
		t.Fatal(err)
	}

	initial, _ := msp045Capture(t, handler)
	issue54AssertNoRemoteEvidence(t, initial)

	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: remoteSKI}})
	visible, _ := msp045Capture(t, handler)
	if len(visible.Services) != 1 || !visible.Services[0].Visible {
		t.Fatalf("visible callback services = %+v, want one visible service", visible.Services)
	}
	if len(visible.Sessions) != 0 {
		t.Fatalf("visible callback fabricated sessions: %+v", visible.Sessions)
	}

	handler.RemoteSKIConnected(nil, remoteSKI)
	connected, _ := msp045Capture(t, handler)
	if len(connected.Services) != 1 || !connected.Services[0].Visible {
		t.Fatalf("connection callback lost visible service: %+v", connected.Services)
	}
	if len(connected.Sessions) != 1 || connected.Sessions[0].State != "connected" {
		t.Fatalf("connection callback sessions = %+v, want one connected session", connected.Sessions)
	}
}

func issue54ProtectedStoreInstance(t *testing.T, stateRoot string, material runtimeMaterial) [sha256.Size]byte {
	t.Helper()
	if material.firstTrust == nil {
		t.Fatal("protected material omitted first-trust store binding")
	}
	bridge, outcome := openRuntimeAssociationBridge(stateRoot, material.firstTrust.keyProviders)
	if bridge == nil {
		t.Fatalf("open protected store = %q", outcome)
	}
	defer func() {
		if err := bridge.Close(); err != nil {
			t.Errorf("close protected store: %v", err)
		}
	}()
	view, outcome := bridge.ReloadControl(context.Background())
	if outcome != "opened_current" && outcome != "opened_migrated" {
		t.Fatalf("reload protected store = %q", outcome)
	}
	if view.control.storeInstance == [sha256.Size]byte{} {
		t.Fatal("protected store instance is empty")
	}
	return view.control.storeInstance
}

func issue54ExpectedNodeToken(storeInstance [sha256.Size]byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("helianthus-eebus-node-v1\x00"))
	_, _ = digest.Write(storeInstance[:])
	return hex.EncodeToString(digest.Sum(nil)[:16])
}

func issue54MaterialNodeToken(t *testing.T, material runtimeMaterial) string {
	t.Helper()
	field := reflect.ValueOf(material).FieldByName("nodeToken")
	if !field.IsValid() || field.Kind() != reflect.String {
		t.Fatal("runtime material omitted the canonical node token")
	}
	return field.String()
}

func issue54Configuration(t *testing.T, material runtimeMaterial) *eebusapi.Configuration {
	t.Helper()
	service, err := newEEBusService(RuntimeConfig{Interface: "fixture-interface", ListenPort: 4711}, material, nil)
	if err != nil {
		t.Fatalf("construct production service: %v", err)
	}
	t.Cleanup(service.Shutdown)
	configured, ok := service.(interface {
		Configuration() *eebusapi.Configuration
	})
	if !ok || configured.Configuration() == nil {
		t.Fatal("production service omitted its eeBUS configuration")
	}
	return configured.Configuration()
}

func issue54AssertCanonicalConfiguration(t *testing.T, configuration *eebusapi.Configuration, nodeToken string) {
	t.Helper()
	if got := configuration.VendorCode(); got != "Project-Helianthus" {
		t.Fatalf("vendor code = %q", got)
	}
	if got := configuration.DeviceBrand(); got != "Helianthus" {
		t.Fatalf("brand = %q", got)
	}
	if got := configuration.DeviceModel(); got != "eebusreg" {
		t.Fatalf("model = %q", got)
	}
	if got := configuration.DeviceSerialNumber(); got != nodeToken {
		t.Fatalf("serial number = %q, want %q", got, nodeToken)
	}
	if got := configuration.Identifier(); got != "HLS-"+nodeToken {
		t.Fatalf("SHIP ID = %q, want %q", got, "HLS-"+nodeToken)
	}
	if got := configuration.MdnsServiceName(); got != "Helianthus EnergyManagementSystem eebusreg" {
		t.Fatalf("mDNS service name = %q", got)
	}
	if got := configuration.DeviceType(); got != spinemodel.DeviceTypeTypeEnergyManagementSystem {
		t.Fatalf("device type = %q", got)
	}
	if got := configuration.EntityTypes(); !slices.Equal(got, []spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM}) {
		t.Fatalf("entity types = %v", got)
	}
}

func issue54AssertNoRemoteEvidence(t *testing.T, snapshot runtimeSnapshotPayload) {
	t.Helper()
	if len(snapshot.Pairing) != 0 || len(snapshot.Services) != 0 || len(snapshot.Sessions) != 0 || len(snapshot.Topology.Devices) != 0 {
		t.Fatalf("policy fabricated remote evidence: pairing=%+v services=%+v sessions=%+v topology=%+v", snapshot.Pairing, snapshot.Services, snapshot.Sessions, snapshot.Topology)
	}
}
