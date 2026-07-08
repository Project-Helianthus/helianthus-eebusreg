# Raw Identity Redaction Gate

Status: `required-for-MSP-02A`

## Threats

Raw runtime identity can expose stable device and session identifiers:

- local SKI;
- remote SKI;
- certificate fingerprints;
- peer identifiers;
- pairing state;
- session identifiers;
- unknown protocol values that may contain identity-bearing bytes.

## Gate

The MSP-02A public API passes this gate only if:

- stable identifiers are represented by `RedactedID`;
- unknown protocol values are represented by `OpaqueValue`;
- public identity labels are closed allowlists (`IDKind` and `UnknownPath`),
  not arbitrary caller-controlled strings;
- `RedactID` and `OpaqueBytes` are intake helpers only; callers must not log,
  persist, or expose their raw arguments before conversion;
- JSON marshaling validates the contract before output;
- `String()`, `GoString()`, and formatting for redacted/opaque values never
  include raw input;
- tests prove representative raw inputs do not appear in JSON or `%v`/`%+v`/
  `%#v` formatting output;
- CI rejects public listener, trust-store, pairing-window, and trust-mutation
  surfaces, including forbidden names declared inside Go blocks.

## Evidence

Validation commands:

```bash
./scripts/ci_local.sh
```

Relevant tests:

- `TestJSONAndStringDoNotLeakRawInputs`
- `TestCallerControlledKindAndPathAreRejected`
- `TestOpaqueFormattingDoesNotLeakRawFields`
- `TestStandaloneRedactedValuesRejectUnsafeJSON`
- `TestIdentityDocumentRejectsUnredactedStableID`
- `TestIdentityDocumentRejectsUnknownFieldRawValue`

The gate is intentionally limited to the MSP-02A contract. Snapshot hashing,
evidence dereference, trust-store persistence, runtime facade integration, and
MCP authorization are later milestones.
