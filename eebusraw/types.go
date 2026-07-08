package eebusraw

import (
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
)

type SKI string

type ServiceID string

type DeviceID string

type EntityPath string

type FeaturePath string

type SessionID string

type PairingStateName string

const (
	PairingNoLocalIdentity PairingStateName = "NO_LOCAL_IDENTITY"
	PairingUnpairedLocked  PairingStateName = "UNPAIRED_LOCKED"
	PairingWindowOpen      PairingStateName = "PAIRING_WINDOW_OPEN"
	PairingPairedTrusted   PairingStateName = "PAIRED_TRUSTED"
	PairingRevoked         PairingStateName = "REVOKED"
	PairingCorruptStore    PairingStateName = "CORRUPT_STORE"
	PairingQuarantined     PairingStateName = "QUARANTINED"
)

type PairingState struct {
	State      PairingStateName
	WindowOpen bool
	Candidate  *RemoteCandidate
	UpdatedAt  time.Time
	Degraded   []DegradedState
}

type RemoteCandidate struct {
	SKI         SKI
	Fingerprint string
	ObservedAt  time.Time
	ExpiresAt   time.Time
}

type Snapshot struct {
	RuntimeID       string
	Contract        string
	LocalSKI        SKI
	VisibleServices []Service
	PairedServices  []Service
	Sessions        []Session
	RemoteDevices   []RemoteDevice
	Entities        []Entity
	Features        []Feature
	UsecaseClaims   []UsecaseClaim
	Pairing         PairingState
	EvidenceRefs    []eebusevidence.Ref
	Degraded        []DegradedState
	DataTimestamp   time.Time
	CapturedAt      time.Time
}

type Service struct {
	ID            ServiceID
	RemoteSKI     SKI
	RemoteSHIPID  string
	Name          string
	Manufacturer  string
	LastSeenAt    time.Time
	EvidenceRefs  []eebusevidence.Ref
	UnknownFields []UnknownField
}

type Session struct {
	ID           SessionID
	RemoteSKI    SKI
	State        string
	OpenedAt     time.Time
	LastSeenAt   time.Time
	EvidenceRefs []eebusevidence.Ref
}

type RemoteDevice struct {
	ID            DeviceID
	RemoteSKI     SKI
	Label         string
	EvidenceRefs  []eebusevidence.Ref
	UnknownFields []UnknownField
}

type Entity struct {
	Path          EntityPath
	RemoteSKI     SKI
	Description   string
	EvidenceRefs  []eebusevidence.Ref
	UnknownFields []UnknownField
}

type Feature struct {
	Path          FeaturePath
	EntityPath    EntityPath
	Type          string
	Properties    []Property
	EvidenceRefs  []eebusevidence.Ref
	UnknownFields []UnknownField
}

type Property struct {
	Path          string
	Value         Value
	EvidenceRefs  []eebusevidence.Ref
	UnknownFields []UnknownField
}

type Value struct {
	Kind string
	Text string
}

type UsecaseClaim struct {
	Name         string
	Version      string
	RemoteSKI    SKI
	EvidenceRefs []eebusevidence.Ref
}

type UnknownField struct {
	Path        string
	Encoding    string
	ValueHash   string
	EvidenceRef eebusevidence.Ref
	ObservedAt  time.Time
}

type DegradedState struct {
	Code       string
	Message    string
	Since      time.Time
	RetryAfter time.Time
}
