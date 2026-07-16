package eebusstore

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"
)

const (
	currentSchemaVersion   uint64 = 2
	currentManifestVersion uint64 = 1
	currentSlotVersion     uint64 = 1
)

type generationMetadata struct {
	sequence       uint64
	parentSequence *uint64
	parentSHA256   *string
}

type protectedKeyReference struct {
	providerID            string
	providerVersion       uint64
	sealedBlob            []byte
	certificateSPKISHA256 string
}

type localIdentityV1 struct {
	certificateChainDER [][]byte
	keyReference        protectedKeyReference
	localSKI            []byte
}

type remoteIdentityV1 struct {
	recordID     []byte
	remoteSKI    []byte
	remoteSHIPID string
}

type stateV1 struct {
	localIdentity    *localIdentityV1
	remoteIdentities []remoteIdentityV1
	controlEnvelope  []byte
}

type generationV1 struct {
	metadata      generationMetadata
	state         stateV1
	schemaVersion uint64
}

type generationReference struct {
	generation       uint64
	generationFile   string
	generationSHA256 string
	schemaVersion    uint64
}

type manifestPayloadV1 struct {
	manifestVersion uint64
	current         generationReference
	parent          *generationReference
}

type manifestEnvelope struct {
	slotFormatVersion uint64
	manifestEpoch     uint64
	manifestPayload   []byte
	manifestSHA256    string
}

func (m generationMetadata) String() string   { return "eebusstore.generation_metadata{redacted}" }
func (m generationMetadata) GoString() string { return m.String() }
func (m generationMetadata) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, m.String())
}

func (g generationV1) String() string   { return "eebusstore.generation{redacted}" }
func (g generationV1) GoString() string { return g.String() }
func (g generationV1) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, g.String())
}

func (s stateV1) String() string   { return "eebusstore.state{redacted}" }
func (s stateV1) GoString() string { return s.String() }
func (s stateV1) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, s.String())
}

func (i localIdentityV1) String() string   { return "eebusstore.local_identity{redacted}" }
func (i localIdentityV1) GoString() string { return i.String() }
func (i localIdentityV1) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, i.String())
}

func (i remoteIdentityV1) String() string   { return "eebusstore.remote_identity{redacted}" }
func (i remoteIdentityV1) GoString() string { return i.String() }
func (i remoteIdentityV1) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, i.String())
}

func (r protectedKeyReference) String() string   { return "eebusstore.key_reference{redacted}" }
func (r protectedKeyReference) GoString() string { return r.String() }
func (r protectedKeyReference) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, r.String())
}

func (r generationReference) String() string   { return "eebusstore.generation_reference{redacted}" }
func (r generationReference) GoString() string { return r.String() }
func (r generationReference) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, r.String())
}

func (m manifestPayloadV1) String() string   { return "eebusstore.manifest_payload{redacted}" }
func (m manifestPayloadV1) GoString() string { return m.String() }
func (m manifestPayloadV1) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, m.String())
}

func (m manifestEnvelope) String() string   { return "eebusstore.manifest_envelope{redacted}" }
func (m manifestEnvelope) GoString() string { return m.String() }
func (m manifestEnvelope) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, m.String())
}

func writeRedacted(state fmt.State, verb rune, value string) {
	if verb == 'q' {
		value = strconv.Quote(value)
	}
	_, _ = io.WriteString(state, value)
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:])
}

func generationFilename(sequence uint64) string {
	return fmt.Sprintf("g-%020d.json", sequence)
}
