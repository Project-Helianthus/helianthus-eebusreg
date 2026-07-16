package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

func TestMSP04CAdminDecodesExactRevocationBinding(t *testing.T) {
	fixture := newMSP04CFixture(t)
	want := msp04cRevocationRequest(fixture, 301, 1)
	command, err := decodeFirstTrustAdminCommand(msp04cAdminPayload(t, msp04cRevocationFields(want)))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := command.revocationRequest()
	if command.name != "revoke_association" || !ok || !reflect.DeepEqual(got, want) {
		t.Fatal("revocation command did not preserve its complete exact binding")
	}

	fields := msp04cRevocationFields(want)
	for field := range fields {
		if field == "version" || field == "command" {
			continue
		}
		t.Run(field, func(t *testing.T) {
			incomplete := msp04cCloneAdminFields(fields)
			delete(incomplete, field)
			if _, err := decodeFirstTrustAdminCommand(msp04cAdminPayload(t, incomplete)); err == nil {
				t.Fatalf("binding without %s was accepted", field)
			}
		})
	}
}

func TestMSP04CAdminDecodesExactRepairBindingForClosedKinds(t *testing.T) {
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	kinds := []string{
		"reconcile_pending_publication",
		"publish_inactive_parent",
		"adopt_copied_current",
		"recover_unavailable_host_key",
		"release_retry_quarantine",
	}
	for index, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			want := msp04cExactRepairRequest(fixture, coordinator, kind, uint64(320+index))
			fields := msp04cRepairFields(want)
			command, err := decodeFirstTrustAdminCommand(msp04cAdminPayload(t, fields))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := command.repairRequest()
			if command.name != "repair" || !ok || !reflect.DeepEqual(got, want) {
				t.Fatal("repair command did not preserve its complete exact binding")
			}
			for field := range fields {
				if field == "version" || field == "command" {
					continue
				}
				incomplete := msp04cCloneAdminFields(fields)
				delete(incomplete, field)
				if _, err := decodeFirstTrustAdminCommand(msp04cAdminPayload(t, incomplete)); err == nil {
					t.Fatalf("binding without %s was accepted", field)
				}
			}
		})
	}

	fields := msp04cRepairFields(msp04cExactRepairRequest(fixture, coordinator, kinds[0], 340))
	fields["repair_kind"] = "unlisted"
	if _, err := decodeFirstTrustAdminCommand(msp04cAdminPayload(t, fields)); err == nil {
		t.Fatal("unlisted repair kind was accepted")
	}
}

func TestMSP04CAdminRejectsUnknownFieldsAndOperationReuse(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, lineage, true, true, true, true)}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	handler := &firstTrustAdminHandler{coordinator: coordinator, random: &msp04cOrdinalReader{next: 600}}
	request := msp04cRevocationRequest(fixture, 351, 1)
	fields := msp04cRevocationFields(request)

	unknown := msp04cCloneAdminFields(fields)
	unknown["extra"] = uint64(1)
	if got := msp04cAdminOutcome(t, handler.handle(context.Background(), msp04cAdminPayload(t, unknown))); got != "invalid_command" {
		t.Fatalf("unknown-field outcome = %q", got)
	}
	if fixture.store.calls() != 0 {
		t.Fatal("unknown-field request reached durable state")
	}

	if got := msp04cAdminOutcome(t, handler.handle(context.Background(), msp04cAdminPayload(t, fields))); got != "revoked" {
		t.Fatalf("revocation outcome = %q", got)
	}
	if got := msp04cAdminOutcome(t, handler.handle(context.Background(), msp04cAdminPayload(t, fields))); got != "revoked" {
		t.Fatalf("terminal replay outcome = %q", got)
	}
	changed := msp04cCloneAdminFields(fields)
	changed["expected_control_epoch"] = request.expectedControlEpoch + 1
	if got := msp04cAdminOutcome(t, handler.handle(context.Background(), msp04cAdminPayload(t, changed))); got != "idempotency_conflict" {
		t.Fatalf("changed-binding replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatalf("admin replay publication count = %d, want 1", fixture.store.calls())
	}
}

func TestMSP04CAdminOrdinaryRepliesRemainRedacted(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.anchor.openOutcome = "host_binding_mismatch"
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	handler := &firstTrustAdminHandler{coordinator: coordinator, random: &msp04cOrdinalReader{next: 700}}
	payload := handler.handle(context.Background(), msp04cAdminPayload(t, map[string]any{
		"version": uint64(firstTrustAdminVersion), "command": "status",
	}))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"correlation", "outcome", "recovery_reason", "recovery_state", "state"}
	if !slices.Equal(names, want) {
		t.Fatalf("ordinary status fields = %v, want %v", names, want)
	}
	if got := msp04cAdminString(t, fields, "recovery_state"); got != "QUARANTINED" {
		t.Fatalf("status recovery state = %q", got)
	}
	if got := msp04cAdminString(t, fields, "recovery_reason"); got != "HOST_BINDING_MISMATCH" {
		t.Fatalf("status recovery reason = %q", got)
	}
	first := msp04cOrdinal(1)
	second := msp04cOrdinal(2)
	if bytes.Contains(payload, first[:]) || bytes.Contains(payload, second[:]) {
		t.Fatal("ordinary status contains an opaque fixture value")
	}
}

func TestMSP04CAdminCommandsReuseOnlyTheAuthenticatedLocalTransport(t *testing.T) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "first_trust_admin.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	imports := make([]string, 0, len(parsed.Imports))
	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		imports = append(imports, path)
	}
	for _, forbidden := range []string{"net", "net/http"} {
		if slices.Contains(imports, forbidden) {
			t.Fatalf("admin command layer imports forbidden transport %q", forbidden)
		}
	}

	parsed, err = parser.ParseFile(files, "first_trust_admin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	startCalls := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Start" {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && identifier.Name == "eebusadmin" {
			startCalls++
		}
		return true
	})
	if startCalls != 1 {
		t.Fatalf("authenticated admin transport start calls = %d, want 1", startCalls)
	}
	if _, err := os.Stat("../eebusadmin/admin_red_test.go"); err != nil {
		t.Fatal("same-UID transport conformance test is unavailable")
	}
}

func msp04cRevocationFields(request firstTrustRevocationRequest) map[string]any {
	return map[string]any{
		"version":                      uint64(firstTrustAdminVersion),
		"command":                      "revoke_association",
		"operation_id":                 hex.EncodeToString(request.operationID[:]),
		"association_ref":              hex.EncodeToString(request.associationRef[:]),
		"association_lineage":          hex.EncodeToString(request.associationLineage[:]),
		"expected_generation_sequence": request.expectedGeneration.sequence,
		"expected_generation_filename": request.expectedGeneration.filename,
		"expected_generation_sha256":   hex.EncodeToString(request.expectedGeneration.sha256[:]),
		"expected_generation_schema":   request.expectedGeneration.schemaVersion,
		"expected_manifest_epoch":      request.expectedManifestEpoch,
		"expected_manifest_sha256":     hex.EncodeToString(request.expectedManifestSHA256[:]),
		"expected_control_epoch":       request.expectedControlEpoch,
	}
}

func msp04cRepairFields(request firstTrustRepairRequest) map[string]any {
	fields := map[string]any{
		"version":                      uint64(firstTrustAdminVersion),
		"command":                      "repair",
		"operation_id":                 hex.EncodeToString(request.operationID[:]),
		"repair_kind":                  request.kind,
		"scope":                        hex.EncodeToString(request.scope[:]),
		"expected_state":               request.expectedState,
		"expected_reason":              request.expectedReason,
		"expected_manifest_epoch":      request.expectedManifest.epoch,
		"expected_manifest_sha256":     hex.EncodeToString(request.expectedManifest.sha256[:]),
		"expected_current_sequence":    request.expectedManifest.current.sequence,
		"expected_current_filename":    request.expectedManifest.current.filename,
		"expected_current_sha256":      hex.EncodeToString(request.expectedManifest.current.sha256[:]),
		"expected_current_schema":      request.expectedManifest.current.schemaVersion,
		"expected_control_epoch":       request.expectedControlEpoch,
		"expected_anchor_version":      request.expectedAnchorVersion,
		"expected_manifest_high_water": request.expectedManifestHighWater,
		"expected_control_high_water":  request.expectedControlHighWater,
		"next_repair_sequence":         request.nextRepairSequence,
	}
	if request.expectedManifest.parent != nil {
		fields["expected_parent_sequence"] = request.expectedManifest.parent.sequence
		fields["expected_parent_filename"] = request.expectedManifest.parent.filename
		fields["expected_parent_sha256"] = hex.EncodeToString(request.expectedManifest.parent.sha256[:])
		fields["expected_parent_schema"] = request.expectedManifest.parent.schemaVersion
	}
	return fields
}

func msp04cCloneAdminFields(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func msp04cAdminPayload(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func msp04cAdminOutcome(t *testing.T, payload []byte) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	return msp04cAdminString(t, fields, "outcome")
}

func msp04cAdminString(t *testing.T, fields map[string]json.RawMessage, name string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(fields[name], &value); err != nil {
		t.Fatal(err)
	}
	return value
}
