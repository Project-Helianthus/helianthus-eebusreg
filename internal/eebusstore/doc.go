// Package eebusstore persists the local eeBUS identity, trust associations and their control
// records in an owner-only directory tree on Linux and macOS. Each change is written as a new
// immutable generation and published by renaming a manifest under an exclusive writer lock, so a
// reader sees either the previous or the next complete generation. Opening verifies layout,
// ownership, permissions, ACLs and canonical encoding and returns a named outcome; when the
// current generation is invalid, a digest-verified parent is only offered as a recovery
// candidate, never adopted silently. Local private keys are stored only sealed by a registered
// key provider. Other platforms report filesystem_capability_unavailable. See
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/architecture.
package eebusstore
