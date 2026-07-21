package eebusstore

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	emptyGenerationSHA256     = "ecbc32f0c7652e36457babf82189f88d37da389fb9d228fd346f4064d7b565dd"
	childGenerationSHA256     = "d3109878b7c025524c2418212878fba21ebff307c6c09727dbf0d29d7b1b8968"
	populatedGenerationSHA256 = "9c5cbf5b39eb9951ce74fee28f66c4ced7dfc9cbc14c1a7a0d87e198e21798a4"
	emptyManifestSHA256       = "52c4f3d826edb94faa906ed6609e320052adf7ea7de99d6b484198c5af06ba50"
	childManifestSHA256       = "256fdd2a8b2653b7b14efed9c573d925d189e4c82b5d0cd29239d227a7f85170"
)

type testGenerationRef struct {
	sequence uint64
	sha256   string
	schema   uint64
}

type treeEntry struct {
	mode os.FileMode
	data []byte
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func testDigestHex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func testGenerationFilename(sequence uint64) string {
	return fmt.Sprintf("g-%020d.json", sequence)
}

func testManifestPayloadBytes(current testGenerationRef, parent *testGenerationRef, version uint64) []byte {
	currentJSON := testGenerationRefJSON(current)
	parentJSON := "null"
	if parent != nil {
		parentJSON = testGenerationRefJSON(*parent)
	}
	return []byte(fmt.Sprintf("{\"current\":%s,\"manifest_version\":%d,\"parent\":%s}\n", currentJSON, version, parentJSON))
}

func testGenerationRefJSON(ref testGenerationRef) string {
	return fmt.Sprintf(
		"{\"generation\":%d,\"generation_file\":%s,\"generation_sha256\":%s,\"schema_version\":%d}",
		ref.sequence,
		strconv.Quote(testGenerationFilename(ref.sequence)),
		strconv.Quote(ref.sha256),
		ref.schema,
	)
}

func testManifestSlotBytes(epoch, slotVersion uint64, payload []byte) []byte {
	return []byte(fmt.Sprintf(
		"{\"manifest_epoch\":%d,\"manifest_payload\":%s,\"manifest_sha256\":%s,\"slot_format_version\":%d}\n",
		epoch,
		strconv.Quote(base64.StdEncoding.EncodeToString(payload)),
		strconv.Quote(testDigestHex(payload)),
		slotVersion,
	))
}

func installStoreLayout(t *testing.T, root string, generations map[uint64][]byte, slotA, slotB []byte) {
	t.Helper()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "generations"), 0o700); err != nil {
		t.Fatal(err)
	}
	testWritePrivateFile(t, filepath.Join(root, "LOCK"), nil)
	sequences := make([]uint64, 0, len(generations))
	for sequence := range generations {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	for _, sequence := range sequences {
		testWritePrivateFile(t, filepath.Join(root, "generations", testGenerationFilename(sequence)), generations[sequence])
	}
	if slotA != nil {
		testWritePrivateFile(t, filepath.Join(root, "MANIFEST.A"), slotA)
	}
	if slotB != nil {
		testWritePrivateFile(t, filepath.Join(root, "MANIFEST.B"), slotB)
	}
}

func installTwoGenerationStore(t *testing.T, root string, second []byte) {
	t.Helper()
	first := readFixture(t, "generation-v1-empty.json")
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
	secondRef := testGenerationRef{sequence: 2, sha256: testDigestHex(second), schema: currentSchemaVersion}
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first, 2: second},
		testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
		testManifestSlotBytes(2, 1, testManifestPayloadBytes(secondRef, &firstRef, 1)),
	)
}

func testWritePrivateFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testSnapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	snapshot := map[string]treeEntry{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := treeEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			entry.data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		snapshot[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertTreeEqual(t *testing.T, got, want map[string]treeEntry) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("store tree changed\ngot:  %s\nwant: %s", summarizeTree(got), summarizeTree(want))
	}
}

func summarizeTree(tree map[string]treeEntry) string {
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var summary strings.Builder
	for _, path := range paths {
		entry := tree[path]
		fmt.Fprintf(&summary, "%s:%s:%d:%s;", path, entry.mode, len(entry.data), testDigestHex(entry.data))
	}
	return summary.String()
}

func testStoreConfig(t *testing.T, root string, hook syscallHook, providers map[providerKey]protectedKeyProvider) storeConfig {
	t.Helper()
	backend, err := newNativeSyscallBackend(hook)
	if err != nil {
		t.Fatalf("native backend: %v", err)
	}
	return storeConfig{
		root:      root,
		backend:   backend,
		providers: providers,
	}
}

func openForTest(t *testing.T, root string, hook syscallHook, providers map[providerKey]protectedKeyProvider) openResult {
	t.Helper()
	return openStore(testStoreConfig(t, root, hook, providers))
}

func closeStore(t *testing.T, result openResult) {
	t.Helper()
	if result.store == nil {
		return
	}
	if err := result.store.close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func assertOutcome(t *testing.T, got, want outcome) {
	t.Helper()
	if got != want {
		t.Fatalf("outcome = %q, want %q", got, want)
	}
}

func assertErrorOutcome(t *testing.T, err error, want outcome) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want outcome %q", want)
	}
	assertOutcome(t, outcomeOf(err), want)
}

func syntheticSPKI(t *testing.T) []byte {
	t.Helper()
	spki, err := base64.StdEncoding.DecodeString("MCowBQYDK2VwAyEAIVL40Zt5HSRFMkLhXy6rbLfP+ntqXtMAl5YOBpiB2xI=")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParsePKIXPublicKey(spki); err != nil {
		t.Fatalf("parse synthetic public key: %v", err)
	}
	return spki
}

func syntheticPublicKey() crypto.PublicKey {
	spki, err := base64.StdEncoding.DecodeString("MCowBQYDK2VwAyEAIVL40Zt5HSRFMkLhXy6rbLfP+ntqXtMAl5YOBpiB2xI=")
	if err != nil {
		panic(err)
	}
	publicKey, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		panic(err)
	}
	return publicKey
}

type recordingSigner struct {
	publicKey      crypto.PublicKey
	publicOverride crypto.PublicKey
	calls          *[]string
}

func (s *recordingSigner) Public() crypto.PublicKey {
	*s.calls = append(*s.calls, "public")
	if s.publicOverride != nil {
		return s.publicOverride
	}
	return s.publicKey
}

func (s *recordingSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	*s.calls = append(*s.calls, "sign")
	return bytes.Clone(digest), nil
}

type fakeProtectedKeyProvider struct {
	calls        []string
	probeErr     error
	validateErr  error
	unsealErr    error
	signer       crypto.Signer
	providerID   string
	version      uint64
	sealedBlob   []byte
	expectedSPKI []byte
}

func (p *fakeProtectedKeyProvider) probe(providerID string, version uint64) error {
	p.calls = append(p.calls, "probe")
	p.providerID = providerID
	p.version = version
	return p.probeErr
}

func (p *fakeProtectedKeyProvider) validate(sealedBlob, expectedSPKI []byte) error {
	p.calls = append(p.calls, "validate")
	p.sealedBlob = bytes.Clone(sealedBlob)
	p.expectedSPKI = bytes.Clone(expectedSPKI)
	return p.validateErr
}

func (p *fakeProtectedKeyProvider) unseal(sealedBlob []byte) (crypto.Signer, error) {
	p.calls = append(p.calls, "unseal")
	if !bytes.Equal(p.sealedBlob, sealedBlob) {
		return nil, fmt.Errorf("unseal input differs from validated blob")
	}
	if p.unsealErr != nil {
		return nil, p.unsealErr
	}
	return p.signer, nil
}
