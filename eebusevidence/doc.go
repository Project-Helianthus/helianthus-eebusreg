// Package eebusevidence defines versioned envelopes for redacted evidence metadata.
// References bind a runtime and read context; object digests retain links to raw
// bytes without embedding them. Canonical content hashes exclude capture time.
// Validation rejects unsupported versions and malformed metadata; formatting
// redacts envelope values, while JSON exposes the validated metadata.
package eebusevidence
