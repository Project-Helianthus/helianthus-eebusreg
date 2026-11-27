package inbound

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"

	digest "crypto/sha256"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

const driverKind = "eebus-inbound"

type nativeProofBody struct {
	Text string `json:"text"`
	Unit string `json:"unit"`
}

func (nativeProofBody) Validate() error { return nil }

type identityObject struct {
	Scheme string `json:"scheme"`
	Issuer string `json:"issuer"`
	Token  string `json:"token"`
}

func (identityObject) Validate() error { return nil }

func opaqueID(prefix string, parts ...string) string {
	hash := digest.New()
	for _, part := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(part))
	}
	return prefix + hex.EncodeToString(hash.Sum(nil))
}

func entityText(address []uint64) string {
	if len(address) == 0 {
		return ""
	}
	parts := make([]string, len(address))
	for i, part := range address {
		parts[i] = strconv.FormatUint(part, 10)
	}
	return strings.Join(parts, ".")
}

func deviceSlot(device string) string {
	return "device/" + device
}

func entitySlot(device, entity string) string {
	return "device/" + device + "/entity/" + entity
}

func proofFor(text, unit string) (semreg.EvidenceRef, error) {
	sum, err := semreg.DigestRecord(nativeProofBody{Text: text, Unit: unit})
	if err != nil {
		return semreg.EvidenceRef{}, err
	}
	return semreg.EvidenceRef{
		Owner: "eebus.inbound", Kind: "native.observation", Contract: "eebus.inbound/v1",
		Digest: sum, Access: semreg.EvidenceAccessPublic, Redaction: semreg.RedactionNone,
	}, nil
}

func schemeForToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	catalog, err := graph.Embedded()
	if err != nil {
		return "", false
	}
	for _, scheme := range catalog.IdentificationSchemes() {
		if scheme.IssuerRegistry != graph.IssuerNone {
			continue
		}
		if tokenFits(scheme.TokenForm, token) {
			return string(scheme.ID), true
		}
	}
	return "", false
}

func tokenFits(form, token string) bool {
	switch form {
	case "12 upper-case hexadecimal digits, no separators":
		return len(token) == 12 && upperHex(token)
	case "16 upper-case hexadecimal digits, no separators":
		return len(token) == 16 && upperHex(token)
	default:
		return false
	}
}

func upperHex(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func identityDigest(scheme, token string) (semreg.Digest, error) {
	return semreg.DigestRecord(identityObject{Scheme: scheme, Issuer: "", Token: token})
}
