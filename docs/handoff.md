# Astra session handoff

Last updated: 2026-09-05. This is the live resume document referenced by
`AGENTS.md`. The user requested that work be checkpointed and committed for a
new Astra session; implementation was paused for that handoff.

## Start here

Resume update (2026-09-05): the EPERM cause is now reproduced and fixed in the
working tree on the existing branch. An unsandboxed `ps` diagnostic showed the
leader in `Z` state before the final group SIGKILL returned EPERM. Darwin's
`killpg1` skips zombies. The fix accepts that signal error only on Darwin and
only after successful direct-child cleanup, reap, and independent quiescence
confirmation. Linux EPERM handling is unchanged. Pending exit notifications
are now consumed before `Wait` to avoid a Linux `waitid(WNOWAIT)`/reap race.
Focused cancellation and refused-delivery/active-descendant regressions pass;
`make fmt-check vet test race` passed before the final status-37 test extension.
Repeated verification, cross-builds, independent reviews, and CI for the new
source are still pending. The historical checkpoint details below remain for
comparison and will be replaced by final evidence as this session completes.

The MVP is feature-complete, but **the current checkpoint is not ready to
merge or release**. The latest observer-cleanup changes fix the focused
observer-error cases but introduce a reproducible macOS cancellation failure.
Start with that failure, not another feature or a release build.

Read `AGENTS.md`, `README.md`, `SECURITY.md`, and `docs/architecture.md`.
Then inspect the saved checkpoint:

```sh
git status --short --branch
git log -6 --oneline
git diff b4430b4..HEAD -- internal/app docs/architecture.md
```

All pending source changes are being saved with this handoff in a local WIP
commit whose parent is `b4430b4a6520274618aea856eae3f6fffd0fb4eb`.
Resolve the checkpoint SHA with `git rev-parse HEAD`; commit status is not a
release approval. The handoff commit message is
`wip(exec): checkpoint observer cleanup and Astra handoff`.

## Repository and remote state

- Workspace: `/Users/sohil/Development/vaultctx`.
- Branch: `codex/fix-fish-shell-integration`.
- Remote: `git@github.com:sohilkaushal/vaultctx.git`.
- PR: [#2](https://github.com/sohilkaushal/vaultctx/pull/2), last inspected as open.
- Last observed remote PR head: `adda5bd2486a29b463bdf4c7c1e0841604c9999d`.
  The handoff checkpoint and local `b0dbb69`/`b4430b4` have not been pushed.
  Recheck GitHub before acting; remote observations are from this session.
- Base: `origin/main` at `94cf22d5c92f9a60a683e62be8acbf2da2910abc`,
  the merged PR #1. Local `main` was stale; use the verified remote base.
- PR #2 title/body still describe only the Fish fix. Rewrite them around the
  final Fish and process-lifecycle changes before the eventual push.

## Immediate blocker: final SIGKILL returns EPERM

The current `terminateProcessGroup` captures a failed final group SIGKILL as
`errProcessCleanupIncomplete`. On this macOS host,
`TestExecCancellationPreservesSignalCause` now returns 1 instead of 130/143:

```text
process_unix_test.go:329: canceled exec code = 1, want 130
process cleanup incomplete: process group <pid>:
send SIGKILL to process group: operation not permitted
```

All three subcases (plain cancellation, SIGINT, SIGTERM) failed in the ordinary
test build. The failure also reproduced once outside the Codex sandbox, so it
cannot be dismissed as a sandbox-only artifact. The race build passed once;
that timing-dependent pass does not resolve the ordinary-build failure.

Smallest reproduction, using a task-specific cache:

```sh
GOCACHE=/private/tmp/vaultctx-astra-gocache \
GOMODCACHE=/private/tmp/vaultctx-astra-gomodcache \
go test ./internal/app -run '^TestExecCancellationPreservesSignalCause$' -count=1
```

Confirmed: the final signal's EPERM is now promoted to a cleanup error.
Unconfirmed hypothesis: the child may already have exited from the graceful
signal when the final group signal runs. Determine the actual macOS process
state and signal semantics before choosing a fix. Preserve the signal-before-
reap ordering and prove group quiescence.

The Linux `processGroupActiveAfterProbe` rule that treats EPERM from
`kill(-pgid, 0)` as an active group is a separate, reviewed security invariant.
Do not weaken that probe rule to fix this final-SIGKILL issue.

## What is implemented

| Checkpoint | Change and review state |
| --- | --- |
| `94cf22d` (merged PR #1) | MVP plus agent guide, Linux zombie-aware cleanup, EPERM probe fix, and honest doctor platform reporting. |
| `6ba6285` + `adda5bd` (remote PR #2) | Fish 3.7 empty-value normalization and option terminator; path-list and leading-hyphen regressions. Local reviewers approved this checkpoint for CI. |
| `b0dbb69` (local) | Darwin registration-time kqueue ESRCH becomes a ready exit notification; other registration errors and retrieval errors still report failure. Review found a cancellation/descendant gap. |
| `b4430b4` (local) | A successful exit notification concurrent with cancellation cleans descendants before reaping and preserves the real child status, including CLI status 37. Subsequent review rejected observer-error abort paths. |
| Current WIP checkpoint | Observer-error aborts now kill, reap, and confirm group quiescence; preserve observer diagnostics during cancellation; expose cleanup errors. Focused tests pass, but ordinary cancellation fails as described above. |

The current patch changes these files:

- `internal/app/process_unix.go`: shared quiescence confirmation,
  `abortAfterObserverFailure`, error-returning abort, separate wait/observer/
  cleanup results, and injectable signal/probe functions. Ordinary cancellation
  also now reports final SIGKILL errors and pending observer errors.
- `internal/app/process_commands.go`: maps to context cancellation only when
  the returned error actually matches `ctx.Err()`; cleanup errors and real
  child exit statuses retain priority.
- `internal/app/process_unix_test.go`: bounded registration/retrieval failure
  cases with cancellation and heartbeat descendants, plus injected signal and
  probe failures. These package-global seams require nonparallel tests.
- `docs/architecture.md`: records intended observer-error cleanup behavior.
  It describes implementation intent, not proof that this checkpoint is ready.
- `docs/handoff.md`: this resume state.

The injected signal-error test first delivers the real SIGKILL and then returns
EPERM. It tests diagnostic priority, not a real inability to signal or behavior
when the leader has already exited. Extend coverage appropriately during the fix.

Additional review targets, not confirmed findings:

- Linux's exit observer uses `waitid(WNOWAIT)`; inspect ordering of `cmd.Wait`
  and receiving the pending observer result now that observer errors propagate.
- A one-second bound covers group quiescence probing, not every cleanup stage.
  Inspect direct-child fallback and potentially blocking Wait if signals fail.
- Preserve the distinction between already-consumed and pending notifications;
  avoid a second channel receive or a second Wait.

## Verification for the current source

These results apply to the source saved in the WIP checkpoint, before the final
handoff-only rewrite. No source changes were made during the handoff.

| Check | Result |
| --- | --- |
| `make fmt-check`, `make vet`, `git diff --check` | PASS |
| `make test` | FAIL: the three cancellation-signal subcases above; other packages passed |
| `make race` | PASS once, all five packages; `internal/app` 7.960s |
| Focused observer tests, count 10 | PASS, 4.549s |
| Same observer tests under race detector, count 20 | PASS, 9.971s |
| Combined lifecycle tests, count 20 | FAIL, 46.946s; repeated cancellation status 1 instead of 130/143 |
| Isolated cancellation-signal test outside sandbox, count 1 | FAIL, 1.409s; all three subcases |
| Full shuffled repetitions and cross-builds after this patch | Not completed; earlier passes are stale |

Focused passing commands:

```sh
go test ./internal/app -run 'TestExecObserverFailure(TerminatesChild|CleansCanceledDescendants|SurfacesCleanupFailure)$' -count=10
go test -race ./internal/app -run 'TestExecObserverFailure(TerminatesChild|CleansCanceledDescendants|SurfacesCleanupFailure)$' -count=20
```

The broader failed command was:

```sh
go test ./internal/app -run 'Test(ExecObserverFailureTerminatesChild|ExecObserverFailureCleansCanceledDescendants|ExecObserverFailureSurfacesCleanupFailure|ExecCancellationTerminatesChildProcessGroup|ExecCancellationPreservesSignalCause|ExecCancellationKillsSameGroupDescendants|ManagedCommandPreservesExitStatusAfterDarwinRegistrationRace|ManagedCommandCleansDescendantWhenDarwinRegistrationRaceIsCanceled)$' -count=20
```

The interrupted test session was retrieved and completed; its result was
failure, not a passing or still-pending check. Handoff verification used
`/private/tmp/vaultctx-handoff-gocache` and
`/private/tmp/vaultctx-handoff-gomodcache`.

Historical evidence is available in
`git show b4430b4:docs/handoff.md`. That earlier source passed full/race,
shuffle x20, lifecycle x50, Darwin intersection x100, and nine cross-builds.
Those results do not approve the current observer-abort patch.

## Next steps and completion criteria

1. Reproduce and fix the final-SIGKILL EPERM failure. Require normal
   cancellation statuses 130/143, real exit status 37 in the completed-child
   tie, preserved observer diagnostics, and confirmed descendant cleanup.
2. Run the focused regression first, then `make fmt-check`, `make vet`,
   `make test`, and `make race`. After source is stable, run shuffle x20 and
   relevant repeated lifecycle tests, including the observer-error cases.
3. Cross-build with `CGO_ENABLED=0`: Linux amd64/arm64; Darwin amd64/arm64;
   Windows, FreeBSD, OpenBSD, illumos, and Plan 9 amd64. Also compile
   `internal/app` tests for Darwin amd64/arm64 and Linux amd64. Run Linux
   lifecycle regressions in an appropriate environment.
4. Commit the corrected source and obtain independent Standards and
   Specification reviews of that exact commit against the verified base.
   Both reviews of `b4430b4` returned DO NOT SHIP for the observer-abort gap;
   there is no final approval for the current patch. Resolve findings and
   record the current review verdicts before treating it as release-ready.
5. Update PR #2's title/body, push the branch, and require CI to pass on the
   new head, including real Fish and PowerShell integration. Recheck Codex
   review status for the new head; earlier completed reviews are stale.
6. Add `docs/review-report.md` with commit-specific review, tests, and CI
   evidence. Build and smoke-test a fresh internal candidate with
   `make build VERSION=v0.1.0`, then record its checksum.
7. Public publication remains blocked by the absent license. The owner must
   choose it. Also follow SECURITY.md's private-reporting setup requirements.
   A local candidate or commit is not a published release.

Update this file when a blocker, test result, or release decision changes.
The user's latest request was to save and hand off; this session did not push,
merge, publish, or build a new release candidate.

## CI, tooling, and resources

PR #2's last inspected Fish-only head `adda5bd` passed both Fish/PowerShell
jobs, Ubuntu tests, race, and CodeQL. macOS jobs exposed the fast-child race.
Example failed runs:
[push CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33950679765),
[PR CI](https://github.com/sohilkaushal/vaultctx/actions/runs/33950723662).
These are historical results, not CI evidence for local lifecycle commits.

The GitHub Codex connector was functioning: its
[summary comment](https://github.com/sohilkaushal/vaultctx/pull/2#issuecomment-5550068293)
reported Code Review and Security Review complete for `adda5bd`. The lack of
inline review comments did not mean the trigger was ignored.

This host was macOS arm64, Go 1.26.6, with Bash/Zsh, Vault, and fzf installed.
Fish 3.7 was tested in an Ubuntu 24.04 Podman container named
`vaultctx-fish37-debug`; PowerShell ran in GitHub CI. That disposable container
was not removed during the handoff. Inspect before reusing or removing it.
Temporary caches and test artifacts live under `/private/tmp`; they are
rebuildable and are not required to resume. The existing `bin/vaultctx` is a
stale v0.1.0-rc1 and must not be shipped.

If SSH fetch again advertises no refs, an authenticated HTTPS fetch worked:

```sh
git -c credential.helper='!gh auth git-credential' fetch https://github.com/sohilkaushal/vaultctx.git refs/heads/main:refs/remotes/origin/main
```

## Suggested first prompt for Astra

Continue vaultctx from docs/handoff.md on the existing
codex/fix-fish-shell-integration branch. Start with the documented macOS
cancellation EPERM failure in the saved WIP checkpoint. Preserve the security
invariants, complete the required verification and independent reviews, update
PR #2, and keep the handoff current. The current checkpoint is not approved for
release.
