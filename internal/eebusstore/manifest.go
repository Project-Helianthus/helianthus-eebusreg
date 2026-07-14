package eebusstore

import (
	"bytes"
	"errors"
	"fmt"
)

type manifestSlot string

const (
	manifestSlotA manifestSlot = "A"
	manifestSlotB manifestSlot = "B"
)

type selectedManifest struct {
	slot        manifestSlot
	epoch       uint64
	payload     []byte
	envelope    manifestEnvelope
	envelopeRaw []byte
}

func (m selectedManifest) String() string   { return "eebusstore.selected_manifest{redacted}" }
func (m selectedManifest) GoString() string { return m.String() }
func (m selectedManifest) Format(state fmt.State, verb rune) {
	writeRedacted(state, verb, m.String())
}

func selectManifestSlots(slotA, slotB []byte) (selectedManifest, error) {
	a, aValid := publicationValidSlot(manifestSlotA, slotA)
	b, bValid := publicationValidSlot(manifestSlotB, slotB)
	switch {
	case !aValid && !bValid:
		return selectedManifest{}, newStoreError(outcomeNoValidManifest, "select_manifest", errors.New("no publication-valid slot"))
	case aValid && !bValid:
		return a, nil
	case !aValid && bValid:
		return b, nil
	case a.epoch > b.epoch:
		return a, nil
	case b.epoch > a.epoch:
		return b, nil
	case bytes.Equal(a.envelopeRaw, b.envelopeRaw):
		return a, nil
	default:
		return selectedManifest{}, newStoreError(outcomeManifestAmbiguous, "select_manifest", errors.New("equal epochs differ"))
	}
}

func publicationValidSlot(slot manifestSlot, raw []byte) (selectedManifest, bool) {
	if len(raw) == 0 {
		return selectedManifest{}, false
	}
	envelope, err := decodeManifestSlot(raw)
	if err != nil {
		return selectedManifest{}, false
	}
	return selectedManifest{
		slot:        slot,
		epoch:       envelope.manifestEpoch,
		payload:     bytes.Clone(envelope.manifestPayload),
		envelope:    envelope,
		envelopeRaw: bytes.Clone(raw),
	}, true
}

func otherManifestSlot(slot manifestSlot) manifestSlot {
	if slot == manifestSlotA {
		return manifestSlotB
	}
	return manifestSlotA
}

func manifestSlotFilename(slot manifestSlot) string {
	if slot == manifestSlotB {
		return "MANIFEST.B"
	}
	return "MANIFEST.A"
}
