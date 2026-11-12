// Command toolchainproof verifies that go.mod stays buildable by the supported toolchain without
// local substitutions. It fails when the go or toolchain directive, or the active Go version,
// exceeds -max-go (default: -active-go), when go.mod has any replace directive, or when the
// required eeBUS stack module is missing or pinned to a version other than -version. With
// -module-json it also checks a go list -m -json record for the same path, version and absence
// of a replacement. It prints one summary line on success and exits 1 with a reason otherwise.
package main
