# Process cleanup review and verification

Date: 2026-09-05. Reviewed source:
`5c6c3d4df47d02c0cc88934315a795f571df1635`.
Base: `94cf22d5c92f9a60a683e62be8acbf2da2910abc`.
Branch: `codex/fix-fish-shell-integration`.
[PR #2](https://github.com/sohilkaushal/vaultctx/pull/2).

## Problem and correction

The original `go test ./internal/app -run
'^TestExecCancellationPreservesSignalCause$' -count=1` reproduced cancellation
status 1 instead of 130/143, with group-SIGKILL EPERM. An unsandboxed temporary
diagnostic confirmed an unreaped zombie leader before the failing signal.
Apple's [XNU group signaling](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c#L1612-L1621)
skips zombies and can return EPERM when no eligible group member is found.

The Darwin exception requires successful direct cleanup, reaping, and separate
quiescence confirmation. Linux EPERM handling and both platforms' conservative
probes remain unchanged. Review fixes also order observations before reaping,
bound waiting after direct SIGKILL refusal, retain one eventual reaper without
later signals, and preserve available observer diagnostics across reap timeout.

The branch also fixes Fish 3.7 empty activation values/option parsing and
Darwin fast-child registration/cancellation races. No runtime dependencies were
added. Stdout remains machine-safe. No credentials, Vault output, or
passthrough arguments are logged; tests use disposable helpers and fake data.

## Independent reviews

Both reviewers evaluated `git diff
94cf22d5c92f9a60a683e62be8acbf2da2910abc...5c6c3d4` against `AGENTS.md`,
`CONTRIBUTING.md`, `SECURITY.md`, `README.md`, `docs/architecture.md`, and the
user's continuation request plus handoff requirements.

- **Standards/security: PASS**, no actionable P1/P2 findings. Independently
  ran `env GOCACHE=/private/tmp/vaultctx-review-standards-gocache go test
  ./internal/app -run '^TestCleanupReportsRefusedGroupAndDirectSignals$'
  -count=1 -timeout=30s`: PASS, 10.665s.
- **Specification: PASS**, no unresolved P1/P2 findings or scope-creep finding.
  Source review; this reviewer did not independently rerun tests.

Both rejected `9b7c3ff` for terminal observer ordering and unbounded double
signal refusal. Standards approved `e9dcf6d`, but Specification found pending
observer errors lost behind blocked reaping. `5c6c3d4` resolves all findings.
These are local independent reviews, not GitHub Codex or release approval.

## Final source verification

Host: `go version go1.26.6 darwin/arm64`. All local Go commands use:

```sh
export GOCACHE=/private/tmp/vaultctx-eperm-gocache
export GOMODCACHE=/private/tmp/vaultctx-eperm-gomodcache
```

| Command | Result |
| --- | --- |
| `go test ./internal/app -run '^TestCleanupReportsRefusedGroupAndDirectSignals$' -count=1` | PASS, 11.114s |
| `make fmt-check vet test race` | PASS; app test 19.748s, app race 23.816s; all five packages pass |
| `go test -shuffle=on -count=20 ./...` | Running |
| `go test ./internal/app -run 'Test(ExecObserverFailure\|ExecCancellation\|ManagedCommandPreservesExitStatusAfterDarwinRegistrationRace\|ManagedCommandCleansDescendantWhenDarwinRegistrationRaceIsCanceled)' -count=50` | PASS, 131.028s |
| `go test -race ./internal/app -run 'Test(ExecGroupSignalEPERMRequiresQuiescence\|CleanupReportsRefusedGroupAndDirectSignals)$' -count=10` | Running |
| `git diff --check` | Recheck after final evidence rewrite |

Nine CGO-disabled CLI cross-builds pass: Linux amd64/arm64; Darwin amd64/arm64;
Windows, FreeBSD, OpenBSD, illumos, and Plan 9 amd64. App test compilation
passes for Darwin amd64/arm64 and Linux amd64/arm64. Exact command pattern:

```sh
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o /private/tmp/vaultctx-eperm-cross/<os>-<arch> ./cmd/vaultctx
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go test -c -o /private/tmp/vaultctx-eperm-cross/<os>-<arch>.test ./internal/app
```

Linux execution uses the existing Ubuntu 24.04 arm64 Podman container. The
final binary was built with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c
-o /private/tmp/vaultctx-eperm-cross/linux-arm64-final2.test ./internal/app`,
then copied to `/tmp/vaultctx-eperm-final2.test` inside the container.

Focused Linux check: PASS:

```sh
podman exec vaultctx-fish37-debug /tmp/vaultctx-eperm-final2.test -test.run 'Test(DirectCancellationWaitsForPendingLinuxObserver|CleanupReportsRefusedGroupAndDirectSignals)' -test.count=1 -test.timeout=45s
```

Final Linux repetitions: running:

```sh
podman exec vaultctx-fish37-debug /tmp/vaultctx-eperm-final2.test -test.run 'Test(ExecGroupSignalEPERMRequiresQuiescence|ExecObserverFailure|ExecCancellation|ProcessGroupActive|DirectCancellationWaitsForPendingLinuxObserver|CleanupReportsRefusedGroupAndDirectSignals)' -test.count=20 -test.timeout=9m
```

## CI and candidate

The reviewed runtime source is pushed. Push CI, macOS jobs, both Fish/PowerShell
jobs, and CodeQL passed. The PR Ubuntu/oldstable job failed a test-only 50ms
heartbeat assumption; the matching push job passed:
[push CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33955127034),
[PR CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33955129187),
[CodeQL](https://github.com/sohilkaushal/vaultctx/actions/runs/33955127432).
Earlier Fish/PowerShell results do not substitute for these runs.

The `5c6c3d4` shuffle x20 also found a test-cleanup race between TempDir removal
and the intentionally surviving descendant's final write. Test-only follow-up
now polls for actual heartbeat progress with a two-second deadline and confirms
group quiescence during cleanup. Runtime source is unchanged. Fresh gates,
test-only restamps, and CI are pending; failed runs are not counted as passes.

No fresh candidate has yet been built this session. After gates pass, record
version, source commit, smoke checks, and SHA-256 here. Public publication
requires the owner's license decision and private-reporting setup in
`SECURITY.md`.
