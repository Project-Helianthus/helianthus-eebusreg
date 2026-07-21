package eebusstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxManifestEnvelopeBytes = 16 << 10
	maxManifestPayloadBytes  = 8 << 10
	maxGenerationBytes       = 4 << 20
	maxJSONDepth             = 8
	maxCertificateCount      = 16
	maxCertificateBytes      = 1 << 20
	maxSealedBlobBytes       = 256 << 10
	maxOpaqueIDBytes         = 128
	maxRemoteSHIPIDBytes     = 512
	maxRemoteIdentityCount   = 1024
)

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type generationWire struct {
	Control          json.RawMessage        `json:"control"`
	Generation       generationMetadataWire `json:"generation"`
	LocalIdentity    *localIdentityWire     `json:"local_identity"`
	RemoteIdentities []remoteIdentityWire   `json:"remote_identities"`
	SchemaVersion    uint64                 `json:"schema_version"`
}

type generationMetadataWire struct {
	ParentSequence *uint64 `json:"parent_sequence"`
	ParentSHA256   *string `json:"parent_sha256"`
	Sequence       uint64  `json:"sequence"`
}

type localIdentityWire struct {
	CertificateChainDER []string                  `json:"certificate_chain_der"`
	KeyReference        protectedKeyReferenceWire `json:"key_reference"`
	LocalSKI            string                    `json:"local_ski"`
}

type protectedKeyReferenceWire struct {
	CertificateSPKISHA256 string `json:"certificate_spki_sha256"`
	ProviderID            string `json:"provider_id"`
	ProviderVersion       uint64 `json:"provider_version"`
	SealedBlob            string `json:"sealed_blob"`
}

type remoteIdentityWire struct {
	RecordID     string `json:"record_id"`
	RemoteSHIPID string `json:"remote_ship_id"`
	RemoteSKI    string `json:"remote_ski"`
}

type generationReferenceWire struct {
	Generation       uint64 `json:"generation"`
	GenerationFile   string `json:"generation_file"`
	GenerationSHA256 string `json:"generation_sha256"`
	SchemaVersion    uint64 `json:"schema_version"`
}

type manifestPayloadWire struct {
	Current         generationReferenceWire  `json:"current"`
	ManifestVersion uint64                   `json:"manifest_version"`
	Parent          *generationReferenceWire `json:"parent"`
}

type manifestEnvelopeWire struct {
	ManifestEpoch     uint64 `json:"manifest_epoch"`
	ManifestPayload   string `json:"manifest_payload"`
	ManifestSHA256    string `json:"manifest_sha256"`
	SlotFormatVersion uint64 `json:"slot_format_version"`
}

func decodeGenerationV1(payload []byte) (generationV1, error) {
	var result generationV1
	if err := preflightGenerationArrayBounds(payload); err != nil {
		return result, err
	}
	if err := validateCanonicalJSON(payload, maxGenerationBytes, maxJSONDepth); err != nil {
		return result, err
	}
	version := generationSchemaVersion(payload)
	if version != currentSchemaVersion {
		return result, malformed("decode_generation", errors.New("schema version"))
	}
	var wire generationWire
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return result, malformed("decode_generation", err)
	}
	if len(wire.Control) == 0 {
		return result, malformed("decode_generation", errors.New("missing control field"))
	}
	var controlV3 *controlRecordV3
	if !bytes.Equal(wire.Control, []byte("null")) {
		decoded, err := decodeControlRecordV3(wire.Control)
		if err != nil {
			return result, err
		}
		controlV3 = &decoded
	}
	metadata, err := decodeGenerationMetadata(wire.Generation)
	if err != nil {
		return result, err
	}
	state, err := decodeStateV1(wire.LocalIdentity, wire.RemoteIdentities)
	if err != nil {
		return result, err
	}
	if controlV3 != nil {
		encoded, err := encodeControlRecordV3(*controlV3)
		if err != nil {
			return result, err
		}
		state.controlEnvelope = encoded
	}
	result = generationV1{metadata: metadata, state: state, schemaVersion: currentSchemaVersion}
	canonical, err := encodeGenerationV1(result)
	if err != nil {
		return generationV1{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return generationV1{}, malformed("decode_generation", errors.New("noncanonical bytes"))
	}
	return result, nil
}

func preflightGenerationArrayBounds(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxGenerationBytes {
		return malformed("preflight_generation", errors.New("document size"))
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return malformed("preflight_generation", errors.New("generation object"))
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return malformed("preflight_generation", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return malformed("preflight_generation", errors.New("generation key"))
		}
		switch key {
		case "local_identity":
			if err := preflightLocalIdentity(decoder); err != nil {
				return malformed("preflight_generation", err)
			}
		case "remote_identities":
			if err := preflightBoundedArray(decoder, maxRemoteIdentityCount, 2); err != nil {
				return malformed("preflight_generation", err)
			}
		default:
			if err := skipPreflightJSONValue(decoder, 2); err != nil {
				return malformed("preflight_generation", err)
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return malformed("preflight_generation", errors.New("generation object end"))
	}
	return nil
}

func preflightLocalIdentity(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	if token != json.Delim('{') {
		return errors.New("local identity object")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("local identity key")
		}
		if key == "certificate_chain_der" {
			if err := preflightBoundedArray(decoder, maxCertificateCount, 3); err != nil {
				return err
			}
			continue
		}
		if err := skipPreflightJSONValue(decoder, 3); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return errors.New("local identity object end")
	}
	return nil
}

func preflightBoundedArray(decoder *json.Decoder, maximum, depth int) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return errors.New("bounded array")
	}
	count := 0
	for decoder.More() {
		count++
		if count > maximum {
			return errors.New("array count bound")
		}
		if err := skipPreflightJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return errors.New("bounded array end")
	}
	return nil
}

func skipPreflightJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			if _, ok := key.(string); !ok {
				return errors.New("object key")
			}
			if err := skipPreflightJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("object end")
		}
	case '[':
		for decoder.More() {
			if err := skipPreflightJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("array end")
		}
	default:
		return errors.New("unexpected delimiter")
	}
	return nil
}

func encodeGenerationV1(generation generationV1) ([]byte, error) {
	if err := validateGenerationMetadata(generation.metadata); err != nil {
		return nil, err
	}
	if err := validateStateV1(generation.state); err != nil {
		return nil, err
	}
	version := generation.schemaVersion
	if version != currentSchemaVersion {
		return nil, malformed("encode_generation", errors.New("schema version"))
	}
	var control []byte
	if len(generation.state.controlEnvelope) != 0 {
		record, ok := controlRecordV3FromStateV1(generation.state)
		if !ok {
			return nil, malformed("encode_generation", errors.New("control record"))
		}
		encoded, err := encodeControlRecordV3(record)
		if err != nil {
			return nil, err
		}
		control = encoded
	}
	var out strings.Builder
	out.Grow(512)
	out.WriteByte('{')
	out.WriteString(`"control":`)
	if control == nil {
		out.WriteString("null")
	} else {
		out.Write(control)
	}
	out.WriteByte(',')
	out.WriteString(`"generation":{"parent_sequence":`)
	if generation.metadata.parentSequence == nil {
		out.WriteString("null")
	} else {
		out.WriteString(strconv.FormatUint(*generation.metadata.parentSequence, 10))
	}
	out.WriteString(`,"parent_sha256":`)
	if generation.metadata.parentSHA256 == nil {
		out.WriteString("null")
	} else {
		writeCanonicalString(&out, *generation.metadata.parentSHA256)
	}
	out.WriteString(`,"sequence":`)
	out.WriteString(strconv.FormatUint(generation.metadata.sequence, 10))
	out.WriteString(`},"local_identity":`)
	if generation.state.localIdentity == nil {
		out.WriteString("null")
	} else {
		writeLocalIdentity(&out, *generation.state.localIdentity)
	}
	out.WriteString(`,"remote_identities":[`)
	for index, identity := range generation.state.remoteIdentities {
		if index > 0 {
			out.WriteByte(',')
		}
		writeRemoteIdentity(&out, identity)
	}
	out.WriteString(`],"schema_version":`)
	out.WriteString(strconv.FormatUint(version, 10))
	out.WriteByte('}')
	out.WriteByte('\n')
	if out.Len() > maxGenerationBytes {
		return nil, malformed("encode_generation", errors.New("document too large"))
	}
	return []byte(out.String()), nil
}

func generationSchemaVersion(payload []byte) uint64 {
	var header struct {
		SchemaVersion uint64 `json:"schema_version"`
	}
	if err := json.Unmarshal(payload, &header); err != nil {
		return 0
	}
	return header.SchemaVersion
}

func decodeManifestPayloadV1(payload []byte) (manifestPayloadV1, error) {
	var result manifestPayloadV1
	if err := validateCanonicalJSON(payload, maxManifestPayloadBytes, maxJSONDepth); err != nil {
		return result, err
	}
	var wire manifestPayloadWire
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return result, malformed("decode_manifest", err)
	}
	if wire.ManifestVersion != currentManifestVersion {
		return result, malformed("decode_manifest", errors.New("manifest version"))
	}
	current, err := decodeGenerationReference(wire.Current)
	if err != nil {
		return result, err
	}
	var parent *generationReference
	if wire.Parent != nil {
		decoded, err := decodeGenerationReference(*wire.Parent)
		if err != nil {
			return result, err
		}
		parent = &decoded
	}
	result = manifestPayloadV1{manifestVersion: wire.ManifestVersion, current: current, parent: parent}
	canonical, err := encodeManifestPayloadV1(result)
	if err != nil {
		return manifestPayloadV1{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return manifestPayloadV1{}, malformed("decode_manifest", errors.New("noncanonical bytes"))
	}
	return result, nil
}

func decodeManifestVersion(payload []byte) (uint64, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return 0, malformed("decode_manifest_version", err)
	}
	raw, exists := document["manifest_version"]
	if !exists {
		return 0, malformed("decode_manifest_version", errors.New("manifest version missing"))
	}
	var version uint64
	if err := json.Unmarshal(raw, &version); err != nil {
		return 0, malformed("decode_manifest_version", err)
	}
	return version, nil
}

func extractManifestGenerationReferences(payload []byte) ([]generationReference, error) {
	if err := validateCanonicalJSON(payload, maxManifestPayloadBytes, maxJSONDepth); err != nil {
		return nil, err
	}
	if _, err := decodeManifestVersion(payload); err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, malformed("extract_manifest_references", err)
	}
	currentRaw, currentExists := document["current"]
	parentRaw, parentExists := document["parent"]
	if !currentExists || !parentExists {
		return nil, malformed("extract_manifest_references", errors.New("reference fields missing"))
	}
	current, err := decodeForwardGenerationReference(currentRaw)
	if err != nil {
		return nil, err
	}
	references := []generationReference{current}
	if bytes.Equal(parentRaw, []byte("null")) {
		return references, nil
	}
	parent, err := decodeForwardGenerationReference(parentRaw)
	if err != nil {
		return nil, err
	}
	return append(references, parent), nil
}

func decodeForwardGenerationReference(payload []byte) (generationReference, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return generationReference{}, malformed("extract_manifest_references", err)
	}
	required := []string{"generation", "generation_file", "generation_sha256", "schema_version"}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return generationReference{}, malformed("extract_manifest_references", errors.New("reference field missing"))
		}
	}
	var wire generationReferenceWire
	if err := json.Unmarshal(fields["generation"], &wire.Generation); err != nil {
		return generationReference{}, malformed("extract_manifest_references", err)
	}
	if err := json.Unmarshal(fields["generation_file"], &wire.GenerationFile); err != nil {
		return generationReference{}, malformed("extract_manifest_references", err)
	}
	if err := json.Unmarshal(fields["generation_sha256"], &wire.GenerationSHA256); err != nil {
		return generationReference{}, malformed("extract_manifest_references", err)
	}
	if err := json.Unmarshal(fields["schema_version"], &wire.SchemaVersion); err != nil {
		return generationReference{}, malformed("extract_manifest_references", err)
	}
	return decodeGenerationReference(wire)
}

func encodeManifestPayloadV1(manifest manifestPayloadV1) ([]byte, error) {
	if manifest.manifestVersion != currentManifestVersion {
		return nil, malformed("encode_manifest", errors.New("manifest version"))
	}
	if err := validateGenerationReference(manifest.current); err != nil {
		return nil, err
	}
	if manifest.parent != nil {
		if err := validateGenerationReference(*manifest.parent); err != nil {
			return nil, err
		}
	}
	var out strings.Builder
	out.WriteString(`{"current":`)
	writeGenerationReference(&out, manifest.current)
	out.WriteString(`,"manifest_version":1,"parent":`)
	if manifest.parent == nil {
		out.WriteString("null")
	} else {
		writeGenerationReference(&out, *manifest.parent)
	}
	out.WriteString("}\n")
	if out.Len() > maxManifestPayloadBytes {
		return nil, malformed("encode_manifest", errors.New("document too large"))
	}
	return []byte(out.String()), nil
}

func decodeManifestSlot(payload []byte) (manifestEnvelope, error) {
	var result manifestEnvelope
	if err := validateCanonicalJSON(payload, maxManifestEnvelopeBytes, maxJSONDepth); err != nil {
		return result, err
	}
	var wire manifestEnvelopeWire
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return result, malformed("decode_manifest_slot", err)
	}
	if wire.ManifestEpoch == 0 || wire.ManifestEpoch > math.MaxInt64 {
		return result, malformed("decode_manifest_slot", errors.New("manifest epoch"))
	}
	decodedPayload, err := decodeCanonicalBase64(wire.ManifestPayload, 1, maxManifestPayloadBytes)
	if err != nil {
		return result, err
	}
	if !digestPattern.MatchString(wire.ManifestSHA256) || sha256Hex(decodedPayload) != wire.ManifestSHA256 {
		return result, malformed("decode_manifest_slot", errors.New("manifest digest"))
	}
	result = manifestEnvelope{
		slotFormatVersion: wire.SlotFormatVersion,
		manifestEpoch:     wire.ManifestEpoch,
		manifestPayload:   decodedPayload,
		manifestSHA256:    wire.ManifestSHA256,
	}
	canonical, err := encodeManifestSlot(result)
	if err != nil {
		return manifestEnvelope{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return manifestEnvelope{}, malformed("decode_manifest_slot", errors.New("noncanonical bytes"))
	}
	return result, nil
}

func encodeManifestSlot(slot manifestEnvelope) ([]byte, error) {
	if slot.manifestEpoch == 0 || slot.manifestEpoch > math.MaxInt64 || len(slot.manifestPayload) == 0 || len(slot.manifestPayload) > maxManifestPayloadBytes {
		return nil, malformed("encode_manifest_slot", errors.New("invalid envelope"))
	}
	digest := sha256Hex(slot.manifestPayload)
	if slot.manifestSHA256 != "" && slot.manifestSHA256 != digest {
		return nil, malformed("encode_manifest_slot", errors.New("manifest digest"))
	}
	var out strings.Builder
	out.WriteString(`{"manifest_epoch":`)
	out.WriteString(strconv.FormatUint(slot.manifestEpoch, 10))
	out.WriteString(`,"manifest_payload":`)
	writeCanonicalString(&out, base64.StdEncoding.EncodeToString(slot.manifestPayload))
	out.WriteString(`,"manifest_sha256":`)
	writeCanonicalString(&out, digest)
	out.WriteString(`,"slot_format_version":`)
	out.WriteString(strconv.FormatUint(slot.slotFormatVersion, 10))
	out.WriteString("}\n")
	if out.Len() > maxManifestEnvelopeBytes {
		return nil, malformed("encode_manifest_slot", errors.New("document too large"))
	}
	return []byte(out.String()), nil
}

func validateCanonicalJSON(payload []byte, maximum, maximumDepth int) error {
	if len(payload) == 0 || len(payload) > maximum || payload[len(payload)-1] != '\n' || (len(payload) > 1 && payload[len(payload)-2] == '\n') || !utf8.Valid(payload) {
		return malformed("validate_json", errors.New("document framing"))
	}
	if err := validateCanonicalJSONLexemes(payload); err != nil {
		return malformed("validate_json", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 1, maximumDepth); err != nil {
		return malformed("validate_json", err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return malformed("validate_json", errors.New("trailing data"))
	}
	return nil
}

func validateCanonicalJSONLexemes(payload []byte) error {
	inString := false
	for index := 0; index < len(payload)-1; index++ {
		character := payload[index]
		if !inString {
			switch character {
			case '"':
				inString = true
			case ' ', '\t', '\r', '\n':
				return errors.New("insignificant whitespace")
			}
			continue
		}
		switch character {
		case '\\':
			index++
			if index >= len(payload)-1 || (payload[index] != '"' && payload[index] != '\\') {
				return errors.New("noncanonical string escape")
			}
		case '"':
			inString = false
		default:
			if character < 0x20 {
				return errors.New("control character")
			}
		}
	}
	if inString {
		return errors.New("unterminated string")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth, maximumDepth int) error {
	if depth > maximumDepth {
		return errors.New("nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			var previous string
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || !validSchemaString(key) {
					return errors.New("invalid object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return errors.New("duplicate object key")
				}
				if len(seen) > 0 && bytes.Compare([]byte(previous), []byte(key)) >= 0 {
					return errors.New("object key order")
				}
				seen[key] = struct{}{}
				previous = key
				if err := validateJSONValue(decoder, depth+1, maximumDepth); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := validateJSONValue(decoder, depth+1, maximumDepth); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected delimiter")
		}
	case json.Number:
		text := value.String()
		if text == "" || (text != "0" && text[0] == '0') || strings.ContainsAny(text, "+-.eE") {
			return errors.New("noncanonical integer")
		}
		parsed, err := strconv.ParseUint(text, 10, 64)
		if err != nil || parsed > math.MaxInt64 {
			return errors.New("integer range")
		}
	case string:
		if !validSchemaString(value) {
			return errors.New("invalid string")
		}
	case bool, nil:
		return nil
	default:
		return errors.New("invalid JSON value")
	}
	return nil
}

func decodeCanonicalBase64(encoded string, minimum, maximum int) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maximum) {
		return nil, malformed("decode_base64", errors.New("encoded length"))
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) < minimum || len(decoded) > maximum || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, malformed("decode_base64", errors.New("invalid base64"))
	}
	return decoded, nil
}

func decodeClosedJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func decodeGenerationMetadata(wire generationMetadataWire) (generationMetadata, error) {
	metadata := generationMetadata{sequence: wire.Sequence, parentSequence: wire.ParentSequence, parentSHA256: wire.ParentSHA256}
	if err := validateGenerationMetadata(metadata); err != nil {
		return generationMetadata{}, err
	}
	return metadata, nil
}

func validateGenerationMetadata(metadata generationMetadata) error {
	if metadata.sequence == 0 || metadata.sequence > math.MaxInt64 || (metadata.parentSequence == nil) != (metadata.parentSHA256 == nil) {
		return malformed("validate_generation", errors.New("generation metadata"))
	}
	if metadata.parentSequence != nil {
		if *metadata.parentSequence == 0 || *metadata.parentSequence >= metadata.sequence || *metadata.parentSequence > math.MaxInt64 || !digestPattern.MatchString(*metadata.parentSHA256) {
			return malformed("validate_generation", errors.New("parent metadata"))
		}
	}
	return nil
}

func decodeStateV1(local *localIdentityWire, remotes []remoteIdentityWire) (stateV1, error) {
	state := stateV1{remoteIdentities: make([]remoteIdentityV1, 0, len(remotes))}
	if local != nil {
		identity := localIdentityV1{certificateChainDER: make([][]byte, 0, len(local.CertificateChainDER))}
		for _, encoded := range local.CertificateChainDER {
			certificate, err := decodeCanonicalBase64(encoded, 1, maxCertificateBytes)
			if err != nil {
				return stateV1{}, err
			}
			identity.certificateChainDER = append(identity.certificateChainDER, certificate)
		}
		sealedBlob, err := decodeCanonicalBase64(local.KeyReference.SealedBlob, 1, maxSealedBlobBytes)
		if err != nil {
			return stateV1{}, err
		}
		localSKI, err := decodeCanonicalBase64(local.LocalSKI, 1, maxOpaqueIDBytes)
		if err != nil {
			return stateV1{}, err
		}
		identity.localSKI = localSKI
		identity.keyReference = protectedKeyReference{
			providerID:            local.KeyReference.ProviderID,
			providerVersion:       local.KeyReference.ProviderVersion,
			sealedBlob:            sealedBlob,
			certificateSPKISHA256: local.KeyReference.CertificateSPKISHA256,
		}
		state.localIdentity = &identity
	}
	for _, remote := range remotes {
		recordID, err := decodeCanonicalBase64(remote.RecordID, 1, maxOpaqueIDBytes)
		if err != nil {
			return stateV1{}, err
		}
		remoteSKI, err := decodeCanonicalBase64(remote.RemoteSKI, 1, maxOpaqueIDBytes)
		if err != nil {
			return stateV1{}, err
		}
		state.remoteIdentities = append(state.remoteIdentities, remoteIdentityV1{recordID: recordID, remoteSKI: remoteSKI, remoteSHIPID: remote.RemoteSHIPID})
	}
	if err := validateStateV1(state); err != nil {
		return stateV1{}, err
	}
	return state, nil
}

func validateStateV1(state stateV1) error {
	if len(state.remoteIdentities) > maxRemoteIdentityCount {
		return malformed("validate_state", errors.New("remote count"))
	}
	if len(state.controlEnvelope) != 0 && !validControlRecordV3Envelope(state.controlEnvelope) {
		return malformed("validate_state", errors.New("control record"))
	}
	if state.localIdentity != nil {
		identity := state.localIdentity
		if len(identity.certificateChainDER) == 0 || len(identity.certificateChainDER) > maxCertificateCount || len(identity.localSKI) == 0 || len(identity.localSKI) > maxOpaqueIDBytes {
			return malformed("validate_state", errors.New("local identity bounds"))
		}
		total := 0
		for _, certificate := range identity.certificateChainDER {
			if len(certificate) == 0 || len(certificate) > maxCertificateBytes-total {
				return malformed("validate_state", errors.New("certificate bounds"))
			}
			total += len(certificate)
		}
		reference := identity.keyReference
		if !providerIDPattern.MatchString(reference.providerID) || reference.providerVersion == 0 || reference.providerVersion > math.MaxInt64 || len(reference.sealedBlob) == 0 || len(reference.sealedBlob) > maxSealedBlobBytes || !digestPattern.MatchString(reference.certificateSPKISHA256) {
			return malformed("validate_state", errors.New("provider reference"))
		}
	}
	seenRecord := make(map[string]struct{}, len(state.remoteIdentities))
	seenSKI := make(map[string]struct{}, len(state.remoteIdentities))
	seenSHIP := make(map[string]struct{}, len(state.remoteIdentities))
	var previous []byte
	for index, identity := range state.remoteIdentities {
		if len(identity.recordID) == 0 || len(identity.recordID) > maxOpaqueIDBytes || len(identity.remoteSKI) == 0 || len(identity.remoteSKI) > maxOpaqueIDBytes || len(identity.remoteSHIPID) == 0 || len([]byte(identity.remoteSHIPID)) > maxRemoteSHIPIDBytes || !validSchemaString(identity.remoteSHIPID) {
			return malformed("validate_state", errors.New("remote identity bounds"))
		}
		if index > 0 && bytes.Compare(previous, identity.recordID) >= 0 {
			return malformed("validate_state", errors.New("remote record order"))
		}
		previous = identity.recordID
		recordKey := string(identity.recordID)
		skiKey := string(identity.remoteSKI)
		if _, exists := seenRecord[recordKey]; exists {
			return malformed("validate_state", errors.New("duplicate record"))
		}
		if _, exists := seenSKI[skiKey]; exists {
			return malformed("validate_state", errors.New("duplicate ski"))
		}
		if _, exists := seenSHIP[identity.remoteSHIPID]; exists {
			return malformed("validate_state", errors.New("duplicate ship id"))
		}
		seenRecord[recordKey] = struct{}{}
		seenSKI[skiKey] = struct{}{}
		seenSHIP[identity.remoteSHIPID] = struct{}{}
	}
	return nil
}

func decodeGenerationReference(wire generationReferenceWire) (generationReference, error) {
	reference := generationReference{generation: wire.Generation, generationFile: wire.GenerationFile, generationSHA256: wire.GenerationSHA256, schemaVersion: wire.SchemaVersion}
	if err := validateGenerationReference(reference); err != nil {
		return generationReference{}, err
	}
	return reference, nil
}

func validateGenerationReference(reference generationReference) error {
	if reference.generation == 0 || reference.generation > math.MaxInt64 || reference.generationFile != generationFilename(reference.generation) || !digestPattern.MatchString(reference.generationSHA256) || reference.schemaVersion > math.MaxInt64 {
		return malformed("validate_generation_reference", errors.New("invalid reference"))
	}
	return nil
}

func writeLocalIdentity(out *strings.Builder, identity localIdentityV1) {
	out.WriteString(`{"certificate_chain_der":[`)
	for index, certificate := range identity.certificateChainDER {
		if index > 0 {
			out.WriteByte(',')
		}
		writeCanonicalString(out, base64.StdEncoding.EncodeToString(certificate))
	}
	out.WriteString(`],"key_reference":{"certificate_spki_sha256":`)
	writeCanonicalString(out, identity.keyReference.certificateSPKISHA256)
	out.WriteString(`,"provider_id":`)
	writeCanonicalString(out, identity.keyReference.providerID)
	out.WriteString(`,"provider_version":`)
	out.WriteString(strconv.FormatUint(identity.keyReference.providerVersion, 10))
	out.WriteString(`,"sealed_blob":`)
	writeCanonicalString(out, base64.StdEncoding.EncodeToString(identity.keyReference.sealedBlob))
	out.WriteString(`},"local_ski":`)
	writeCanonicalString(out, base64.StdEncoding.EncodeToString(identity.localSKI))
	out.WriteByte('}')
}

func writeRemoteIdentity(out *strings.Builder, identity remoteIdentityV1) {
	out.WriteString(`{"record_id":`)
	writeCanonicalString(out, base64.StdEncoding.EncodeToString(identity.recordID))
	out.WriteString(`,"remote_ship_id":`)
	writeCanonicalString(out, identity.remoteSHIPID)
	out.WriteString(`,"remote_ski":`)
	writeCanonicalString(out, base64.StdEncoding.EncodeToString(identity.remoteSKI))
	out.WriteByte('}')
}

func writeGenerationReference(out *strings.Builder, reference generationReference) {
	out.WriteString(`{"generation":`)
	out.WriteString(strconv.FormatUint(reference.generation, 10))
	out.WriteString(`,"generation_file":`)
	writeCanonicalString(out, reference.generationFile)
	out.WriteString(`,"generation_sha256":`)
	writeCanonicalString(out, reference.generationSHA256)
	out.WriteString(`,"schema_version":`)
	out.WriteString(strconv.FormatUint(reference.schemaVersion, 10))
	out.WriteByte('}')
}

func writeCanonicalString(out *strings.Builder, value string) {
	out.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(character)
		default:
			out.WriteRune(character)
		}
	}
	out.WriteByte('"')
}

func validSchemaString(value string) bool {
	if !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
		return false
	}
	for _, character := range value {
		if !utf8.ValidRune(character) || unicode.Is(unicode.Cc, character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func malformed(operation string, cause error) *storeError {
	return newStoreError(outcomeMalformedState, operation, cause)
}
