# Contributing

Run the complete local gate before requesting review:

```sh
make check
make race
```

Keep the runtime dependency-free unless a dependency solves a concrete problem
that cannot be handled safely with the standard library. Explain the security,
supply-chain, binary-size, and maintenance trade-off in the change description.

## Required review evidence

Every change should include:

- user-visible behavior and compatibility impact;
- tests for success, failure, cancellation, and malformed input;
- confirmation that stdout remains machine-safe and diagnostics stay on stderr;
- a statement about token/MFA/config/Vault-output exposure; and
- an independent review for any stop-ship area listed in `SECURITY.md`.

## Coding conventions

- Run `gofmt`; errors should add concise operation context.
- Never invoke `sh -c` with context data.
- Never log passthrough arguments or child output. An executable name may appear
  in a local resolution/start error.
- Never add arbitrary environment maps, shell hooks, executable hooks, or fzf
  options to the config schema.
- Use exact, bounded parsing and validate returned selector IDs against the
  in-memory context set.
- Preserve child I/O plus numeric and signal-derived exit behavior; keep the
  Unix cancellation/process-group policy covered by subprocess tests.
- Store no credentials—even encrypted credentials—until a separately reviewed
  OS-keychain design exists.

## Test design

Prefer pure package tests, fake binaries, and credential canaries. Tests that
contact Vault must use a disposable TLS-enabled instance and must be opt-in.
Never require a developer's real Vault environment or token.
