# Agent guide

## Mission and boundary

`vaultctx` is a local, dependency-free Go CLI for selecting HashiCorp Vault
connection contexts. It is comparable to `kubectx` plus `fzf`; it is not a
Vault server plugin and it must not implement Vault auth, secrets, or database
backend protocols.

The primary release targets are macOS and Linux. Windows and other Unix builds
must remain compilable, but their weaker filesystem/process guarantees must be
reported honestly rather than presented as equivalent to the primary targets.

Before changing code, read:

1. `README.md` for operator-visible behavior and limitations.
2. `SECURITY.md` for stop-ship areas and the threat boundary.
3. `docs/architecture.md` for package ownership and invariants.
4. `docs/handoff.md` for the live work state, recent findings, and pending
   release gates.

When work changes the current state, update `docs/handoff.md` in the same turn.
Do not leave a passing result, a new blocker, or a release decision only in chat.

## Toolchain and repository shape

- Go version: 1.25 or newer (`go.mod` currently declares `go 1.25.0`).
- Runtime dependencies: standard library only. There is intentionally no
  `go.sum` while there are no external modules.
- Entrypoint: `cmd/vaultctx`.
- Packages:
  - `internal/config`: strict schema, validation, locking, and atomic storage.
  - `internal/contextenv`: environment mapping and shell renderers/wrappers.
  - `internal/selector`: isolated `fzf` and numbered selection.
  - `internal/app`: CLI commands, diagnostics, and managed subprocesses.
- Build/test entrypoints: `Makefile` and `.github/workflows/ci.yml`.

In restricted Codex environments, set `GOCACHE` to a task-specific directory
under `/private/tmp`; the default user Go cache may be unreadable. Use
`apply_patch` for source and documentation edits. Preserve unrelated workspace
changes and do not assume a Git repository exists—check first.

## Non-negotiable security invariants

- Never add token, password, MFA, secret ID, unseal key, license, or arbitrary
  environment fields to the context schema. Metadata fields are not
  secret-scanned, so do not place real operational secrets in tests or docs.
- Never log Vault output, passthrough arguments, credential values, proxy
  values, or trust-store values. Use obvious fake canaries in tests.
- Standard output must remain machine-safe: activation programs or requested
  machine data only. Put warnings and execution notices on standard error.
- Do not execute a shell with context-controlled data. Shell plans must use the
  existing quoting functions and adversarial tests.
- Activation clears all managed Vault connection variables plus token, MFA, and
  headers by default. `--keep-token`, `--forward-ambient-token`, and
  `--allow-token-helper` must remain explicit and destination-bound.
- The default `exec` token sentinel blocks token-helper lookup fallback; it does
  not block helper writes. Do not overstate this in code or docs.
- Friendly names are not a trust boundary. Preserve current/previous
  fingerprints and fail closed after a named context is replaced.
- Bash/Zsh `vctx` is top-level-only, rejects cross-dialect initializers, and
  preflights activation before parent-shell evaluation. Do not remove these
  guards. Fish must update caller-visible bindings while shadowing universal
  variables rather than deleting them. PowerShell errors must propagate.
- `fzf` receives only NUL-delimited display metadata, fixed options, bounded
  output, and the minimal allowlisted environment. Validate its returned name
  against the in-memory context set.
- Config parsing stays bounded, exact-case, duplicate-key rejecting, valid
  UTF-8, versioned, and credential-field-free. Preserve owner/mode/link checks,
  same-directory atomic replacement, directory sync, and process locking.
- macOS/Linux non-interactive `exec` children use a dedicated process group.
  Cancellation must not return while a same-group descendant can still run; an
  inability to confirm cleanup must surface as an error.
- Never claim a platform hardening check that is not implemented. In
  particular, ownership/link verification is currently macOS/Linux-only and
  macOS extended ACLs remain unverified.

Any change involving destination binding, credentials, config integrity, shell
rendering, selector isolation, TLS/proxy behavior, or subprocess lifecycle is a
`SECURITY.md` stop-ship change. It needs adversarial tests and an independent
review before release.

## Required verification

Run the smallest focused regression first, then the complete gate:

```sh
make fmt-check
make vet
make test
make race
```

For release candidates also run shuffled repetitions, relevant high-count
regressions, and CGO-disabled cross-builds listed in `docs/handoff.md`. Bash and
Zsh integration tests run locally when installed. Fish and PowerShell are not
available on every developer host; their real-shell tests in the CI
`shell-integration` job are a required release gate, not an optional check.

Do not mark the MVP shipped when:

- a current P1/P2 review finding is unresolved;
- a final reviewer has not restamped the tree after the last stop-ship edit;
- the post-edit full/race/shuffled gates are stale;
- CI-required Fish/PowerShell tests have not passed for a public release;
- the binary was built before the final source change; or
- public publication is requested but the repository still has no license.

## Release hygiene

Build an internal candidate with `make build VERSION=<version>` only after the
tree is green. Record exact commands and results in a review report under
`docs/`. Do not invent a commit hash, tag, remote, CI result, or reviewer
approval. A local build is not a published release.

The project intentionally has no license at present. Choosing one is an owner
decision with legal consequences; agents must not infer or add a license.
