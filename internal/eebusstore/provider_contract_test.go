package eebusstore

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProtectedKeyProviderProbeValidateUnsealAndCertificateBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	installTwoGenerationStore(t, root, readFixture(t, "generation-v1-populated.json"))
	provider, providers := validProviderRegistry()

	result := openForTest(t, root, nil, providers)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeOpenedCurrent)
	if result.store == nil || result.state == nil {
		t.Fatal("valid protected identity did not return active state")
	}
	wantCalls := []string{"probe", "validate", "unseal", "public"}
	if !reflect.DeepEqual(provider.calls, wantCalls) {
		t.Fatalf("provider calls = %v, want %v", provider.calls, wantCalls)
	}
	if provider.providerID != "test.provider" || provider.version != 7 {
		t.Fatalf("provider identity = %q/%d, want test.provider/7", provider.providerID, provider.version)
	}
	if !bytes.Equal(provider.sealedBlob, []byte("sealed-provider-reference")) {
		t.Fatal("provider did not receive the decoded opaque sealed blob")
	}
	if !bytes.Equal(provider.expectedSPKI, syntheticSPKI(t)) {
		t.Fatal("provider validation did not bind to the leaf certificate SPKI")
	}
}

func TestProtectedKeyProviderFailuresAreClosedAndClassified(t *testing.T) {
	tests := map[string]struct {
		configure func(*fakeProtectedKeyProvider)
		providers bool
		want      outcome
	}{
		"missing provider": {
			providers: false,
			want:      outcomeKeyProviderUnavailable,
		},
		"provider capability unavailable": {
			providers: true,
			configure: func(provider *fakeProtectedKeyProvider) {
				provider.probeErr = errors.New("synthetic capability unavailable")
			},
			want: outcomeKeyProviderUnavailable,
		},
		"wrong host": {
			providers: true,
			configure: func(provider *fakeProtectedKeyProvider) {
				provider.validateErr = errors.New("synthetic wrong host")
			},
			want: outcomeKeyMaterialUnavailable,
		},
		"unseal unavailable": {
			providers: true,
			configure: func(provider *fakeProtectedKeyProvider) {
				provider.unsealErr = errors.New("synthetic unseal failure")
			},
			want: outcomeKeyMaterialUnavailable,
		},
		"signer public key mismatch": {
			providers: true,
			configure: func(provider *fakeProtectedKeyProvider) {
				provider.signer = &recordingSigner{
					publicKey:      syntheticPublicKey(),
					publicOverride: ed25519.PublicKey(bytes.Repeat([]byte{0x43}, ed25519.PublicKeySize)),
					calls:          &provider.calls,
				}
			},
			want: outcomeKeyMaterialUnavailable,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			installTwoGenerationStore(t, root, readFixture(t, "generation-v1-populated.json"))
			provider, registry := validProviderRegistry()
			if test.configure != nil {
				test.configure(provider)
			}
			if !test.providers {
				registry = nil
			}

			result := openForTest(t, root, nil, registry)
			assertOutcome(t, result.outcome, test.want)
			if result.store != nil || result.state != nil || result.recovery != nil {
				t.Fatal("protected-key failure returned state or recovery content")
			}
		})
	}
}

func TestProtectedKeyCertificateDigestMismatchFailsClosed(t *testing.T) {
	populated := readFixture(t, "generation-v1-populated.json")
	mismatched := bytes.Replace(populated, []byte("9a82517f9af19416d98fdbcf193726b3a95c0b6fec1d51884bf3e1b739ba2ef4"), []byte(strings.Repeat("0", 64)), 1)
	root := filepath.Join(t.TempDir(), "store")
	installTwoGenerationStore(t, root, mismatched)
	_, providers := validProviderRegistry()

	result := openForTest(t, root, nil, providers)
	assertOutcome(t, result.outcome, outcomeKeyMaterialUnavailable)
	if result.store != nil || result.state != nil {
		t.Fatal("certificate digest mismatch returned active state")
	}
}

func TestKeyBearingCommitRevalidatesProviderAndMatchesGoldenBytes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	provider, providers := validProviderRegistry()
	opened := openForTest(t, root, nil, providers)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	provider.calls = nil

	populated, err := decodeGenerationV1(readFixture(t, "generation-v1-populated.json"))
	if err != nil {
		t.Fatal(err)
	}
	committed := opened.store.commit(populated.state)
	assertOutcome(t, committed.outcome, outcomeCommitDurable)
	if want := []string{"probe", "validate", "unseal", "public"}; !reflect.DeepEqual(provider.calls, want) {
		t.Fatalf("commit provider calls = %v, want %v", provider.calls, want)
	}
	closeStore(t, opened)

	payload := readFixture(t, "generation-v1-populated.json")
	stored, err := os.ReadFile(filepath.Join(root, "generations", testGenerationFilename(2)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payload) {
		t.Fatal("key-bearing commit bytes differ from populated canonical golden")
	}
}

func validProviderRegistry() (*fakeProtectedKeyProvider, map[providerKey]protectedKeyProvider) {
	provider := &fakeProtectedKeyProvider{}
	provider.signer = &recordingSigner{
		publicKey: syntheticPublicKey(),
		calls:     &provider.calls,
	}
	return provider, map[providerKey]protectedKeyProvider{
		{id: "test.provider", version: 7}: provider,
	}
}
