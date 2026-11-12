// Package eebusinteropsmoke builds the redacted transport-gate evidence of the eeBUS interop
// smoke: a disposable fake-peer handshake, the live advertisement and inbound direct-access
// gates, and the deterministic first-trust artifact. Reports keep completed prerequisite stages
// when a later stage fails, separate operator-observed live evidence from CI replay, and reject
// raw identities, network addresses, key material and capture data before anything is
// serialized. Only the package's own tests drive it; no runtime package imports it.
package eebusinteropsmoke
