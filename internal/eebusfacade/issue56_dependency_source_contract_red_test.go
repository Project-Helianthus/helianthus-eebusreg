package eebusfacade

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

const (
	issue56EEBusGoVersion = "v0.7.1-helianthus.19"
	issue56SHIPGoVersion  = "v0.6.1-helianthus.17"
)

func TestIssue56DependencyPinsAreReleasedDirectAndWorkspaceFree(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]string{
		"github.com/Project-Helianthus/helianthus-eebus-go": issue56EEBusGoVersion,
		"github.com/Project-Helianthus/helianthus-ship-go":  issue56SHIPGoVersion,
	}
	for _, requirement := range file.Require {
		if want, ok := required[requirement.Mod.Path]; ok {
			if requirement.Mod.Version != want || strings.Contains(requirement.Mod.Version, "-0.") {
				t.Fatalf("%s pin = %s, want released %s", requirement.Mod.Path, requirement.Mod.Version, want)
			}
			delete(required, requirement.Mod.Path)
		}
	}
	if len(required) != 0 {
		t.Fatalf("missing dependency pins: %#v", required)
	}
	if len(file.Replace) != 0 {
		t.Fatalf("replace directives are forbidden: %#v", file.Replace)
	}
	if _, err := os.Stat(filepath.Join("..", "..", "go.work")); !os.IsNotExist(err) {
		t.Fatalf("repository-local go.work is forbidden, stat error = %v", err)
	}
}

func TestIssue56SHIPSourceContractPinsBeforeWebSocketAndHasNoEndpointFallback(t *testing.T) {
	root := issue56ModuleDir(t, "github.com/Project-Helianthus/helianthus-ship-go", issue56SHIPGoVersion)
	connections := issue56ReadSource(t, root, "hub", "hub_connections.go")
	pairing := issue56ReadSource(t, root, "hub", "hub_pairing.go")
	api := issue56ReadSource(t, root, "api", "pairing_candidate.go")
	retryAPI := issue56ReadSource(t, root, "api", "trusted_remote_retry.go")

	issue56RequireInOrder(t, connections,
		"func (d *websocketOutgoingAttemptDialer) DialContextExpectedSKI(",
		"tlsConfig.VerifyPeerCertificate = func(",
		"if observedSKI != expectedSKI {",
		"return clone.DialContext(ctx, url, headers)",
	)
	issue56RequireAll(t, connections,
		"connectFoundPairingCandidate(",
		"false,\n\t\ttrue,\n\t\tcandidateAuthority,\n\t\t\"protected SHIP connection\"",
		"DialContextExpectedSKI(permit.Context, address, nil, expectedSKI)",
		"errors.New(\"outgoing dialer does not support expected-SKI pinning\")",
	)
	issue56RequireAll(t, pairing,
		"func (h *Hub) SelectPairingCandidate(candidateRef, expectedSKI string) (api.PairingCandidateReservation, error)",
		"reservation, _, err := h.admitPairingCandidate(candidateRef, expectedSKI, false)",
		"h.consumedPairingCandidates[candidateRef] = struct{}{}",
		"entry, exists := h.visiblePairingCandidates[candidateRef]",
		"func (h *Hub) ConnectPairingCandidate(reservation api.PairingCandidateReservation) error",
		"candidate.reservation.Matches(reservation)",
		"activeCandidate.connectIssued = true",
		"h.launchPairingCandidate(launch)",
	)
	issue56RequireAll(t, api,
		"type PairingCandidateController interface {",
		"SelectPairingCandidate(candidateRef, expectedSKI string) (PairingCandidateReservation, error)",
		"ConnectPairingCandidate(PairingCandidateReservation) error",
		"func (PairingCandidateReservation) MarshalJSON() ([]byte, error)",
		"return nil, ErrPairingCandidateReservationSerialization",
	)
	issue56RequireAll(t, retryAPI,
		"type TrustedRemoteRetryController interface {",
		"RetryTrustedRemote(expectedSKI string) error",
	)
	issue56RequireAll(t, connections,
		"func (h *Hub) RetryTrustedRemote(expectedSKI string) error",
		"observation, observed := h.visibleTrustedRemoteObservations[ski]",
		"func discardTransientPINProvider(remoteSKI string, provider api.TransientPINProvider)",
		"provider.(api.TransientPINDiscarder)",
		"discarder.DiscardTransientPIN(remoteSKI)",
	)
	for _, forbidden := range []string{"host string", "port int", "path string", "endpoint string"} {
		for _, prefix := range []string{
			"SelectPairingCandidate(candidateRef, expectedSKI string, ",
			"ConnectPairingCandidate(reservation PairingCandidateReservation, ",
			"RetryTrustedRemote(expectedSKI string, ",
		} {
			if strings.Contains(api+retryAPI, prefix+forbidden) {
				t.Fatalf("owner mutation accepts static transport coordinate %q", forbidden)
			}
		}
	}
}

func TestIssue56SHIPSourceContractHoldsApprovalAndDisablesSessionResumption(t *testing.T) {
	root := issue56ModuleDir(t, "github.com/Project-Helianthus/helianthus-ship-go", issue56SHIPGoVersion)
	connection := issue56ReadSource(t, root, "ship", "connection.go")
	access := issue56ReadSource(t, root, "ship", "hs_access.go")
	connections := issue56ReadSource(t, root, "hub", "hub_connections.go")
	listener := issue56ReadSource(t, root, "hub", "listener_policy.go")

	issue56RequireAll(t, connection,
		"requirePairingApproval",
		"pairingApprovalReleased",
		"SPINE data before pairing approval",
	)
	issue56RequireInOrder(t, access,
		"Save the SHIP ID.",
		"reportServiceShipID",
		"publishPairingApproved",
	)
	issue56RequireAll(t, listener, "SessionTicketsDisabled: true")
	if strings.Contains(connections, "ClientSessionCache:") {
		t.Fatal("outbound SHIP dialer enables TLS client session resumption")
	}
}

func TestIssue56EEBusGoSourceContractKeepsCandidateCallbackOptionalAndOrdered(t *testing.T) {
	root := issue56ModuleDir(t, "github.com/Project-Helianthus/helianthus-eebus-go", issue56EEBusGoVersion)
	api := issue56ReadSource(t, root, "api", "api.go")
	hub := issue56ReadSource(t, root, "service", "service_hub.go")
	service := issue56ReadSource(t, root, "service", "service.go")

	issue56RequireAll(t, api,
		"type PairingCandidateReader interface {",
		"VisiblePairingCandidatesUpdated(service ServiceInterface, candidates []shipapi.PairingCandidateRef)",
		"type PairingCandidateQueuer interface {",
		"QueuePairingCandidate(candidateRef, expectedSKI string) error",
		"type PairingCandidateController interface {",
		"SelectPairingCandidate(candidateRef, expectedSKI string) (shipapi.PairingCandidateReservation, error)",
		"ConnectPairingCandidate(reservation shipapi.PairingCandidateReservation) error",
		"type TrustedRemoteRetryController interface {",
		"RetryTrustedRemote(expectedSKI string) error",
	)
	issue56RequireInOrder(t, hub,
		"reader, ok := s.serviceHandler.(api.PairingCandidateReader)",
		"cloneAndSortPairingCandidateRefs(candidates)",
		"event.reader.VisiblePairingCandidatesUpdated(service, event.candidates)",
	)
	issue56RequireAll(t, service,
		"func (s *Service) SelectPairingCandidate(",
		"return controller.SelectPairingCandidate(candidateRef, expectedSKI)",
		"func (s *Service) ConnectPairingCandidate(reservation shipapi.PairingCandidateReservation) error",
		"return controller.ConnectPairingCandidate(reservation)",
		"func (s *Service) RetryTrustedRemote(expectedSKI string) error",
		"return controller.RetryTrustedRemote(expectedSKI)",
	)
}

func issue56ModuleDir(t *testing.T, module, version string) string {
	t.Helper()
	command := exec.Command("go", "list", "-mod=readonly", "-m", "-json", module)
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", module, err, output)
	}
	var info struct {
		Path    string
		Version string
		Dir     string
		Replace *struct {
			Path string
		}
	}
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatal(err)
	}
	if info.Path != module || info.Version != version || info.Dir == "" || info.Replace != nil {
		t.Fatalf("module provenance = path:%q version:%q dir:%q replace:%v", info.Path, info.Version, info.Dir, info.Replace)
	}
	return info.Dir
}

func issue56ReadSource(t *testing.T, root string, elements ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, elements...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func issue56RequireAll(t *testing.T, source string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(source, fragment) {
			t.Fatalf("reviewed source omits contract fragment %q", fragment)
		}
	}
}

func issue56RequireInOrder(t *testing.T, source string, fragments ...string) {
	t.Helper()
	position := 0
	for _, fragment := range fragments {
		next := strings.Index(source[position:], fragment)
		if next < 0 {
			t.Fatalf("reviewed source omits ordered contract fragment %q", fragment)
		}
		position += next + len(fragment)
	}
}
