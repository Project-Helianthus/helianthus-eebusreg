//go:build linux || darwin

package eebusfacade

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	nativeWitnessKeyBytes       = 32
	nativeWitnessMaximumAnchor  = 64 << 10
	nativeWitnessMaximumClass   = 64
	nativeWitnessMaximumName    = 128
	nativeWitnessAnchorVersion  = 1
	nativeWitnessKeyFileVersion = 1
)

var (
	errNativeWitnessUnavailable = errors.New("protected runtime material: host witness unavailable")
	nativeWitnessProcessMu      sync.Mutex
)

type nativeProtectedWitness struct {
	stateRoot string
	root      string
	key       [sha256.Size]byte
}

type nativeWitnessAnchorWire struct {
	Version                     uint64                    `json:"version"`
	AnchorIdentity              string                    `json:"anchor_identity"`
	StoreInstance               string                    `json:"store_instance"`
	ManifestGenerationHighWater uint64                    `json:"manifest_generation_high_water"`
	ControlEpochHighWater       uint64                    `json:"control_epoch_high_water"`
	Pending                     *nativeWitnessPendingWire `json:"pending"`
}

type nativeWitnessPendingWire struct {
	OperationID          string                    `json:"operation_id"`
	OperationClass       string                    `json:"operation_class"`
	StoreInstance        string                    `json:"store_instance"`
	PreviousControlEpoch uint64                    `json:"previous_control_epoch"`
	TargetControlEpoch   uint64                    `json:"target_control_epoch"`
	PreviousManifest     nativeWitnessManifestWire `json:"previous_manifest"`
	TargetManifest       nativeWitnessManifestWire `json:"target_manifest"`
}

type nativeWitnessManifestWire struct {
	Epoch   uint64                       `json:"epoch"`
	SHA256  string                       `json:"sha256"`
	Current nativeWitnessGenerationWire  `json:"current"`
	Parent  *nativeWitnessGenerationWire `json:"parent"`
}

type nativeWitnessGenerationWire struct {
	Sequence      uint64 `json:"sequence"`
	Filename      string `json:"filename"`
	SHA256        string `json:"sha256"`
	SchemaVersion uint64 `json:"schema_version"`
}

func openNativeProtectedWitness(stateRoot string, machineIdentity [sha256.Size]byte) (*nativeProtectedWitness, error) {
	clean := filepath.Clean(strings.TrimSpace(stateRoot))
	if clean == "." || clean == "" || !filepath.IsAbs(clean) || machineIdentity == [sha256.Size]byte{} {
		return nil, errNativeWitnessUnavailable
	}
	canonical, err := filepath.EvalSymlinks(clean)
	if err != nil || canonical != clean {
		return nil, errNativeWitnessUnavailable
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, errNativeWitnessUnavailable
	}
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || strings.ContainsAny(base, `/\\`) {
		return nil, errNativeWitnessUnavailable
	}
	witness := &nativeProtectedWitness{
		stateRoot: clean,
		root:      filepath.Join(filepath.Dir(clean), "."+base+"-eebus-host-witness"),
	}
	if err := ensureNativeWitnessDirectory(witness.root); err != nil {
		return nil, errNativeWitnessUnavailable
	}
	var hostSecret [nativeWitnessKeyBytes]byte
	if err := witness.withLock(func() error {
		loaded, loadErr := readNativeWitnessFile(filepath.Join(witness.root, "KEY"), 5+nativeWitnessKeyBytes)
		switch {
		case loadErr == nil:
			if len(loaded) != 5+nativeWitnessKeyBytes || !bytes.Equal(loaded[:4], []byte("HWKY")) || loaded[4] != nativeWitnessKeyFileVersion {
				return errNativeWitnessUnavailable
			}
			copy(hostSecret[:], loaded[5:])
		case errors.Is(loadErr, os.ErrNotExist):
			if _, err := io.ReadFull(rand.Reader, hostSecret[:]); err != nil || hostSecret == [nativeWitnessKeyBytes]byte{} {
				return errNativeWitnessUnavailable
			}
			payload := make([]byte, 5+nativeWitnessKeyBytes)
			copy(payload[:4], "HWKY")
			payload[4] = nativeWitnessKeyFileVersion
			copy(payload[5:], hostSecret[:])
			if err := writeNativeWitnessFileAtomic(witness.root, "KEY", payload, true); err != nil {
				return err
			}
		default:
			return loadErr
		}
		return nil
	}); err != nil {
		clear(hostSecret[:])
		return nil, errNativeWitnessUnavailable
	}
	witness.key = deriveNativeWitnessKey(hostSecret, machineIdentity, clean)
	clear(hostSecret[:])
	if witness.key == [sha256.Size]byte{} {
		return nil, errNativeWitnessUnavailable
	}
	return witness, nil
}

func (witness *nativeProtectedWitness) keyMaterial() [sha256.Size]byte {
	if witness == nil {
		return [sha256.Size]byte{}
	}
	return witness.key
}

func (witness *nativeProtectedWitness) loadAnchor() (firstTrustAnchorRecord, bool, error) {
	if witness == nil || witness.key == [sha256.Size]byte{} {
		return firstTrustAnchorRecord{}, false, errNativeWitnessUnavailable
	}
	var result firstTrustAnchorRecord
	found := false
	err := witness.withLock(func() error {
		payload, err := readNativeWitnessFile(filepath.Join(witness.root, "ANCHOR"), nativeWitnessMaximumAnchor)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		decoded, err := decodeNativeWitnessAnchor(payload, witness.key)
		if err != nil {
			return err
		}
		result, found = decoded, true
		return nil
	})
	if err != nil {
		return firstTrustAnchorRecord{}, false, errNativeWitnessUnavailable
	}
	return cloneFirstTrustAnchorRecord(result), found, nil
}

func (witness *nativeProtectedWitness) compareAndStoreAnchor(expected *firstTrustAnchorRecord, target firstTrustAnchorRecord) error {
	if witness == nil || witness.key == [sha256.Size]byte{} || !validNativeWitnessAnchor(target) {
		return errNativeWitnessUnavailable
	}
	return witness.withLock(func() error {
		payload, readErr := readNativeWitnessFile(filepath.Join(witness.root, "ANCHOR"), nativeWitnessMaximumAnchor)
		var current firstTrustAnchorRecord
		present := false
		if readErr == nil {
			var err error
			current, err = decodeNativeWitnessAnchor(payload, witness.key)
			if err != nil {
				return err
			}
			present = true
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if expected == nil {
			if present {
				return errNativeWitnessUnavailable
			}
		} else if !present || !firstTrustAnchorRecordEqual(current, *expected) {
			return errNativeWitnessUnavailable
		}
		encoded, err := encodeNativeWitnessAnchor(target, witness.key)
		if err != nil {
			return err
		}
		return writeNativeWitnessFileAtomic(witness.root, "ANCHOR", encoded, false)
	})
}

func deriveNativeWitnessKey(hostSecret [nativeWitnessKeyBytes]byte, machineIdentity [sha256.Size]byte, stateRoot string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, hostSecret[:])
	_, _ = io.WriteString(mac, "helianthus-eebusreg/host-witness/v1\x00")
	_, _ = mac.Write(machineIdentity[:])
	_, _ = mac.Write([]byte{0})
	_, _ = io.WriteString(mac, stateRoot)
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func ensureNativeWitnessDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errNativeWitnessUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errNativeWitnessUnavailable
	}
	return nil
}

func (witness *nativeProtectedWitness) withLock(operation func() error) error {
	if witness == nil || operation == nil || ensureNativeWitnessDirectory(witness.root) != nil {
		return errNativeWitnessUnavailable
	}
	nativeWitnessProcessMu.Lock()
	defer nativeWitnessProcessMu.Unlock()
	lockPath := filepath.Join(witness.root, "LOCK")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return errNativeWitnessUnavailable
	}
	defer lock.Close()
	if err := validateNativeWitnessOpenFile(lockPath, lock); err != nil {
		return err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return errNativeWitnessUnavailable
	}
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	}()
	if err := operation(); err != nil {
		return errNativeWitnessUnavailable
	}
	return nil
}

func validateNativeWitnessOpenFile(path string, file *os.File) error {
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm() != 0o600 {
		return errNativeWitnessUnavailable
	}
	fileInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, fileInfo) || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 {
		return errNativeWitnessUnavailable
	}
	return nil
}

func readNativeWitnessFile(path string, maximum int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > int64(maximum) {
		return nil, errNativeWitnessUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errNativeWitnessUnavailable
	}
	defer file.Close()
	if err := validateNativeWitnessOpenFile(path, file); err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(payload) > maximum || int64(len(payload)) != info.Size() {
		return nil, errNativeWitnessUnavailable
	}
	return payload, nil
}

func writeNativeWitnessFileAtomic(root, name string, payload []byte, createOnly bool) error {
	if name != "KEY" && name != "ANCHOR" || len(payload) == 0 {
		return errNativeWitnessUnavailable
	}
	target := filepath.Join(root, name)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || createOnly {
			return errNativeWitnessUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errNativeWitnessUnavailable
	}
	var suffix [12]byte
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		return errNativeWitnessUnavailable
	}
	temporary := filepath.Join(root, "."+name+".tmp-"+hex.EncodeToString(suffix[:]))
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errNativeWitnessUnavailable
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := writeNativeWitnessAll(file, payload); err != nil || file.Sync() != nil || file.Close() != nil {
		return errNativeWitnessUnavailable
	}
	if err := os.Rename(temporary, target); err != nil {
		return errNativeWitnessUnavailable
	}
	removeTemporary = false
	directory, err := os.Open(root)
	if err != nil {
		return errNativeWitnessUnavailable
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errNativeWitnessUnavailable
	}
	return nil
}

func writeNativeWitnessAll(file *os.File, payload []byte) error {
	for len(payload) != 0 {
		count, err := file.Write(payload)
		if err != nil || count <= 0 {
			return errNativeWitnessUnavailable
		}
		payload = payload[count:]
	}
	return nil
}

func encodeNativeWitnessAnchor(anchor firstTrustAnchorRecord, key [sha256.Size]byte) ([]byte, error) {
	if !validNativeWitnessAnchor(anchor) || key == [sha256.Size]byte{} {
		return nil, errNativeWitnessUnavailable
	}
	wire := nativeWitnessAnchorToWire(anchor)
	payload, err := json.Marshal(wire)
	if err != nil || len(payload) == 0 || len(payload) > nativeWitnessMaximumAnchor-40 {
		return nil, errNativeWitnessUnavailable
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = io.WriteString(mac, "helianthus-eebusreg/anchor-witness/v1\x00")
	_, _ = mac.Write(payload)
	result := make([]byte, 8+len(payload)+sha256.Size)
	copy(result[:4], "HWAN")
	binary.BigEndian.PutUint32(result[4:8], uint32(len(payload)))
	copy(result[8:], payload)
	copy(result[8+len(payload):], mac.Sum(nil))
	return result, nil
}

func decodeNativeWitnessAnchor(encoded []byte, key [sha256.Size]byte) (firstTrustAnchorRecord, error) {
	if len(encoded) < 8+sha256.Size || len(encoded) > nativeWitnessMaximumAnchor || !bytes.Equal(encoded[:4], []byte("HWAN")) || key == [sha256.Size]byte{} {
		return firstTrustAnchorRecord{}, errNativeWitnessUnavailable
	}
	length := int(binary.BigEndian.Uint32(encoded[4:8]))
	if length <= 0 || 8+length+sha256.Size != len(encoded) {
		return firstTrustAnchorRecord{}, errNativeWitnessUnavailable
	}
	payload, receivedMAC := encoded[8:8+length], encoded[8+length:]
	mac := hmac.New(sha256.New, key[:])
	_, _ = io.WriteString(mac, "helianthus-eebusreg/anchor-witness/v1\x00")
	_, _ = mac.Write(payload)
	if !hmac.Equal(receivedMAC, mac.Sum(nil)) {
		return firstTrustAnchorRecord{}, errNativeWitnessUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire nativeWitnessAnchorWire
	if err := decoder.Decode(&wire); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return firstTrustAnchorRecord{}, errNativeWitnessUnavailable
	}
	anchor, err := nativeWitnessAnchorFromWire(wire)
	if err != nil || !validNativeWitnessAnchor(anchor) {
		return firstTrustAnchorRecord{}, errNativeWitnessUnavailable
	}
	return anchor, nil
}

func nativeWitnessAnchorToWire(anchor firstTrustAnchorRecord) nativeWitnessAnchorWire {
	wire := nativeWitnessAnchorWire{
		Version: anchor.version, AnchorIdentity: hex.EncodeToString(anchor.anchorIdentity[:]),
		StoreInstance:               hex.EncodeToString(anchor.storeInstance[:]),
		ManifestGenerationHighWater: anchor.manifestGenerationHighWater,
		ControlEpochHighWater:       anchor.controlEpochHighWater,
	}
	if anchor.pending != nil {
		wire.Pending = nativeWitnessPendingToWire(*anchor.pending)
	}
	return wire
}

func nativeWitnessPendingToWire(pending firstTrustPendingPublication) *nativeWitnessPendingWire {
	return &nativeWitnessPendingWire{
		OperationID: hex.EncodeToString(pending.operationID[:]), OperationClass: pending.operationClass,
		StoreInstance:        hex.EncodeToString(pending.storeInstance[:]),
		PreviousControlEpoch: pending.previousControlEpoch, TargetControlEpoch: pending.targetControlEpoch,
		PreviousManifest: nativeWitnessManifestToWire(pending.previousManifest),
		TargetManifest:   nativeWitnessManifestToWire(pending.targetManifest),
	}
}

func nativeWitnessManifestToWire(manifest firstTrustManifestBinding) nativeWitnessManifestWire {
	wire := nativeWitnessManifestWire{Epoch: manifest.epoch, SHA256: hex.EncodeToString(manifest.sha256[:]), Current: nativeWitnessGenerationToWire(manifest.current)}
	if manifest.parent != nil {
		parent := nativeWitnessGenerationToWire(*manifest.parent)
		wire.Parent = &parent
	}
	return wire
}

func nativeWitnessGenerationToWire(generation firstTrustGenerationBinding) nativeWitnessGenerationWire {
	return nativeWitnessGenerationWire{Sequence: generation.sequence, Filename: generation.filename, SHA256: hex.EncodeToString(generation.sha256[:]), SchemaVersion: generation.schemaVersion}
}

func nativeWitnessAnchorFromWire(wire nativeWitnessAnchorWire) (firstTrustAnchorRecord, error) {
	anchorIdentity, err := nativeWitnessDigest(wire.AnchorIdentity)
	if err != nil {
		return firstTrustAnchorRecord{}, err
	}
	storeInstance, err := nativeWitnessDigest(wire.StoreInstance)
	if err != nil {
		return firstTrustAnchorRecord{}, err
	}
	anchor := firstTrustAnchorRecord{version: wire.Version, anchorIdentity: anchorIdentity, storeInstance: storeInstance, manifestGenerationHighWater: wire.ManifestGenerationHighWater, controlEpochHighWater: wire.ControlEpochHighWater}
	if wire.Pending != nil {
		pending, err := nativeWitnessPendingFromWire(*wire.Pending)
		if err != nil {
			return firstTrustAnchorRecord{}, err
		}
		anchor.pending = &pending
	}
	return anchor, nil
}

func nativeWitnessPendingFromWire(wire nativeWitnessPendingWire) (firstTrustPendingPublication, error) {
	operationID, err := nativeWitnessDigest(wire.OperationID)
	if err != nil {
		return firstTrustPendingPublication{}, err
	}
	storeInstance, err := nativeWitnessDigest(wire.StoreInstance)
	if err != nil {
		return firstTrustPendingPublication{}, err
	}
	previous, err := nativeWitnessManifestFromWire(wire.PreviousManifest)
	if err != nil {
		return firstTrustPendingPublication{}, err
	}
	target, err := nativeWitnessManifestFromWire(wire.TargetManifest)
	if err != nil {
		return firstTrustPendingPublication{}, err
	}
	return firstTrustPendingPublication{operationID: operationID, operationClass: wire.OperationClass, storeInstance: storeInstance, previousControlEpoch: wire.PreviousControlEpoch, targetControlEpoch: wire.TargetControlEpoch, previousManifest: previous, targetManifest: target}, nil
}

func nativeWitnessManifestFromWire(wire nativeWitnessManifestWire) (firstTrustManifestBinding, error) {
	digest, err := nativeWitnessDigest(wire.SHA256)
	if err != nil {
		return firstTrustManifestBinding{}, err
	}
	current, err := nativeWitnessGenerationFromWire(wire.Current)
	if err != nil {
		return firstTrustManifestBinding{}, err
	}
	manifest := firstTrustManifestBinding{epoch: wire.Epoch, sha256: digest, current: current}
	if wire.Parent != nil {
		parent, err := nativeWitnessGenerationFromWire(*wire.Parent)
		if err != nil {
			return firstTrustManifestBinding{}, err
		}
		manifest.parent = &parent
	}
	return manifest, nil
}

func nativeWitnessGenerationFromWire(wire nativeWitnessGenerationWire) (firstTrustGenerationBinding, error) {
	digest, err := nativeWitnessDigest(wire.SHA256)
	if err != nil || wire.Sequence == 0 || wire.SchemaVersion == 0 || wire.Filename == "" || len(wire.Filename) > nativeWitnessMaximumName || filepath.Base(wire.Filename) != wire.Filename {
		return firstTrustGenerationBinding{}, errNativeWitnessUnavailable
	}
	return firstTrustGenerationBinding{sequence: wire.Sequence, filename: wire.Filename, sha256: digest, schemaVersion: wire.SchemaVersion}, nil
}

func nativeWitnessDigest(value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) {
		return result, errNativeWitnessUnavailable
	}
	copy(result[:], decoded)
	return result, nil
}

func validNativeWitnessAnchor(anchor firstTrustAnchorRecord) bool {
	if anchor.version != nativeWitnessAnchorVersion || anchor.anchorIdentity == [sha256.Size]byte{} || anchor.storeInstance == [sha256.Size]byte{} {
		return false
	}
	if anchor.pending == nil {
		return true
	}
	pending := anchor.pending
	return pending.operationID != [sha256.Size]byte{} && pending.storeInstance == anchor.storeInstance && pending.operationClass != "" && len(pending.operationClass) <= nativeWitnessMaximumClass && pending.targetControlEpoch == pending.previousControlEpoch+1 && pending.targetManifest.epoch == pending.previousManifest.epoch+1 && pending.previousManifest.current.sequence != 0 && pending.targetManifest.current.sequence != 0 && pending.targetManifest.current.sequence != pending.previousManifest.current.sequence
}
