# Process cleanup review and verification

Completed: 2026-09-06. Reviewed/tested checkpoint:
`5f92942a4f79d206bb55e15bf1a45e7eb76a13f7`.
Runtime source is unchanged from `5c6c3d4`.
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
94cf22d5c92f9a60a683e62be8acbf2da2910abc...5f92942` against `AGENTS.md`,
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
Both reviewers subsequently restamped the test-only corrections at `5f92942`
with no actionable P1/P2 findings. They reviewed those corrections by source
inspection without duplicating the ongoing repetitions.

## Final source verification

Host: `go version go1.26.6 darwin/arm64`. All local Go commands use:

```sh
export GOCACHE=/private/tmp/vaultctx-eperm-gocache
export GOMODCACHE=/private/tmp/vaultctx-eperm-gomodcache
```

| Command | Result |
| --- | --- |
| `go test ./internal/app -run '^TestExecGroupSignalEPERMRequiresQuiescence$/observer_failure=.*/live_descendant$' -count=5` | PASS, 12.498s |
| `make fmt-check vet test race` | PASS; app test 20.657s, app race 23.294s; all five packages pass |
| `go test -shuffle=on -count=20 ./...` | PASS; app 387.692s, all five packages pass |
| `go test ./internal/app -run 'Test(ExecObserverFailure\|ExecCancellation\|ManagedCommandPreservesExitStatusAfterDarwinRegistrationRace\|ManagedCommandCleansDescendantWhenDarwinRegistrationRaceIsCanceled)' -count=50` | PASS, 131.028s |
| `go test -race ./internal/app -run 'Test(ExecGroupSignalEPERMRequiresQuiescence\|CleanupReportsRefusedGroupAndDirectSignals)$' -count=10` | PASS, 158.000s |
| `git diff --check` | PASS |

The count-50 lifecycle run applies to runtime-identical `5c6c3d4`; its selected
tests are unchanged in `5f92942`. The final full/race/shuffle and adversarial
race repetitions above include the subsequent test corrections.

Nine CGO-disabled CLI cross-builds pass: Linux amd64/arm64; Darwin amd64/arm64;
Windows, FreeBSD, OpenBSD, illumos, and Plan 9 amd64. App test compilation
passes for Darwin amd64/arm64 and Linux amd64/arm64.
CLI builds use the identical final runtime source; all four test binaries were
recompiled after the final test corrections (with a `-verified.test` suffix).
Exact command pattern:

```sh
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o /private/tmp/vaultctx-eperm-cross/<os>-<arch> ./cmd/vaultctx
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go test -c -o /private/tmp/vaultctx-eperm-cross/<os>-<arch>.test ./internal/app
```

Linux execution uses the existing Ubuntu 24.04 arm64 Podman container. The
focused binary was built with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c
-o /private/tmp/vaultctx-eperm-cross/linux-arm64-final2.test ./internal/app`,
then copied to `/tmp/vaultctx-eperm-final2.test` inside the container.

Focused Linux check: PASS:

```sh
podman exec vaultctx-fish37-debug /tmp/vaultctx-eperm-final2.test -test.run 'Test(DirectCancellationWaitsForPendingLinuxObserver|CleanupReportsRefusedGroupAndDirectSignals)' -test.count=1 -test.timeout=45s
```

Final Linux repetitions: PASS, using the recompiled
`linux-arm64-verified.test` binary copied into the container:

```sh
podman exec vaultctx-fish37-debug /tmp/vaultctx-eperm-verified.test -test.run 'Test(ExecGroupSignalEPERMRequiresQuiescence|ExecObserverFailure|ExecCancellation|ProcessGroupActive|DirectCancellationWaitsForPendingLinuxObserver|CleanupReportsRefusedGroupAndDirectSignals)' -test.count=20 -test.timeout=9m
```

## CI and candidate

Historical CI on `5c6c3d4`: push CI, macOS jobs, both Fish/PowerShell
jobs, and CodeQL passed. The PR Ubuntu/oldstable job failed a test-only 50ms
heartbeat assumption; the matching push job passed:
[push CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33955127034),
[PR CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33955129187),
[CodeQL](https://github.com/sohilkaushal/vaultctx/actions/runs/33955127432).
Earlier Fish/PowerShell results do not substitute for these runs.

The `5c6c3d4` shuffle x20 also found a test-cleanup race between TempDir removal
and the intentionally surviving descendant's final write. Test-only follow-up
now polls for actual heartbeat progress with a two-second deadline and confirms
group quiescence during cleanup. Runtime source is unchanged. Replacement gates,
test-only restamps, and final CI all pass; failed runs are not counted as passes.

Final CI for exact head `5f92942`: **all checks PASS**:
[push CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33956016461),
[PR CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33956018233),
[CodeQL](https://github.com/sohilkaushal/vaultctx/actions/runs/33956016638).
Both real Fish/PowerShell jobs, all macOS/Linux stable/oldstable jobs, race, and
CodeQL pass. The GitHub Codex summary was rechecked and still names `adda5bd`;
it is not approval of the new head. No new review-request comment was sent.

Fresh internal candidate, built after local gates passed:

```sh
make build VERSION=v0.1.0
go version -m bin/vaultctx
```

- Artifact: `bin/vaultctx`, macOS arm64, Go 1.26.6, version `v0.1.0`.
- Embedded VCS revision: `5f92942a4f79d206bb55e15bf1a45e7eb76a13f7`;
  `vcs.modified=false`.
- SHA-256: `cd1561d8191bf2cb6d05055d5013f7d409cca551a886b133f486f27bf2d7f748`.
- Smoke checks PASS: version/help; isolated temporary config add/list; JSON
  activation address and token clearing; successful exec with empty stdout and
  notice on stderr; SIGTERM cancellation status 143. The fixture used only
  `https://vault.example`, `/usr/bin/true`, and `/bin/sleep`; no Vault contact.

Public publication still requires the owner's license decision and
private-reporting setup in `SECURITY.md`. No merge, tag, or public release
artifact was created. Evidence-only commits after `5f92942` change neither its
runtime/tests nor this candidate's reviewed source provenance.
