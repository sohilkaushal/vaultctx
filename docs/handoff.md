# Current handoff

Last updated: 2026-09-05 (UTC)

## Current outcome

The security-focused MVP is feature-complete, but it is **not yet
release-stamped**. PR #1 merged the agent handoff and Linux process-lifecycle
fixes to `main` as `94cf22d`. Its Go, race, macOS/Linux, and CodeQL checks
passed, but both mandatory `shell-integration` runs failed on Fish 3.7.0.

Work is now on `codex/fix-fish-shell-integration` in PR #2. The Fish activation
guard no longer relies on an empty command substitution that Fish 3.7 drops,
and it uses an explicit option terminator so a leading-hyphen value cannot be
interpreted as a `string join` flag. Both PR #2 `shell-integration` runs now
pass, including Fish and PowerShell.

Those runs exposed a separate macOS fast-child race already present on merged
`main`: `env` or `true` could exit between `cmd.Start` and registration of its
kqueue `NOTE_EXIT` filter. Registration then returned `ESRCH`, which vaultctx
reported as an execution failure instead of reaping the child for its real
status. The local fix treats only registration-time `ESRCH` as an already-exited
child notification; all other registration errors and every retrieval error
remain fail-closed. The post-fix full/race/shuffled gates, lifecycle stress,
cross-build matrix, and isolated CLI stress smoke were green.

Fresh review then caught a cancellation intersection on that new path: an exit
notification consumed after context cancellation could preserve the leader's
status but skip cleanup of a still-running same-group descendant. A
deterministic Darwin regression reproduced the leak, and the current working
tree now cleans and confirms the group before reaping the leader while retaining
its real wait result. The complete post-edit gates, lifecycle stress, cross-build
matrix, and targeted restamp are green. A fresh two-axis review of the committed
tree and a green PR #2 rerun on that head are still required.

The earlier Linux CI failure in process-group cleanup was reproduced and
fixed: killed orphan descendants can remain as zombies when the host PID 1
does not promptly reap them, and `kill(-pgid, 0)` treats that zombie-only,
non-runnable group as existing. Linux cleanup confirmation checks `/proc` state
and remains fail-closed on inspection errors.

A subsequent P1 review found that Linux treated `EPERM` from the kernel's
process-group probe as permission to fall back to `/proc`. A filtered `/proc`
could then hide an inaccessible runnable descendant and produce a false
quiescence result. The Linux probe now preserves the kernel evidence by
reporting the group active immediately on `EPERM`; an adversarial regression
injects that kernel result and verifies it cannot fall through to `/proc`.

Do not rely on the earlier implementation-review `SHIP` verdict: it predates
the latest process-lifecycle and doctor portability changes. Do not ship the
existing `bin/vaultctx`; it was built as `v0.1.0-rc1` before those changes.

## Latest stop-ship findings and fixes

The final-quality reviewer returned `DO NOT SHIP` with two concrete P2s:

1. On macOS, `terminateProcessGroup` could reap the direct child and return a
   fraction too early while a SIGKILLed same-group descendant wrote one more
   heartbeat.
2. `doctor` claimed owner/POSIX checks passed on FreeBSD/OpenBSD and other
   platforms even though `internal/config/file_identity_other.go` makes
   ownership/link validation a no-op there.

The current tree addresses both:

- `internal/app/process_unix.go` now waits, for a bounded one second, until the
  killed process group is quiescent. It sends no signal after reaping the
  leader, avoiding damage if the numeric group ID is reused.
- If group quiescence cannot be confirmed, the code returns an error wrapping
  `errProcessCleanupIncomplete`; `runExec` no longer masks that error as ordinary
  context cancellation.
- `TestExecCancellationKillsSameGroupDescendants` now also asserts that the
  process group has no runnable members when `vaultctx` returns.
- `doctor` reports verified owner/POSIX checks only on Linux, retains the macOS
  extended-ACL warning, and warns that filesystem ownership/hard-link checks
  are unverified on other platforms.
- `TestConfigValidationStatusDoesNotOverclaimUnsupportedPlatforms` covers
  Linux, macOS, Windows, FreeBSD, OpenBSD, and Plan 9 messages.

The subsequent CI review found and addressed one additional lifecycle issue:

- On Linux runners whose PID 1 does not promptly reap orphaned children, the
  same-group descendant test consistently failed after one second even though
  SIGKILL had already made every remaining member a zombie. The implementation
  now distinguishes runnable members from Linux zombies using `/proc`, while
  macOS retains the process-group existence probe. Parser coverage includes
  spaces and closing parentheses in process names, and the end-to-end
  cancellation regression verifies both quiescence and a stopped heartbeat.

The follow-up Codex review found and addressed a P1 in that Linux-specific
logic: `EPERM` from `kill(-pgid, 0)` definitively means a process group exists
but is inaccessible, so the implementation now reports it active without
consulting a potentially incomplete `/proc` view.

The merged PR then exposed a Fish 3.7 compatibility failure in the mandatory
real-shell CI job:

- For a cleared variable, unquoted Fish expansion supplied no values to
  `string join`; Fish 3.7 then removed the nominally empty collected command
  substitution. The generated `test` command received only `= ''` and failed.
- `writeFishAssignment` now captures the joined value in a local variable and
  explicitly normalizes a missing element to the empty string before checking
  the assignment.
- The join uses `--` before its separator. An adversarial `-q` namespace can no
  longer turn into the built-in's quiet flag.
- The real-shell regression covers absent metadata, caller-local bindings,
  universal-variable shadowing, a colon-separated path value, a leading-hyphen
  value, and native command failure propagation.

PR #2 confirmed that fix: both independent `shell-integration` runs passed.
The same workflow then exposed a macOS observer race in fast successful
commands:

- A direct child can exit after `cmd.Start` but before kqueue `EV_ADD` attaches
  its one-shot `NOTE_EXIT` filter. macOS reports `ESRCH` for that attach race.
- The child is still unreaped at that point, so the PID cannot have been reused.
  The observer now returns an immediately ready successful notification and
  lets `cmd.Wait` recover the real exit status.
- The exception is deliberately restricted to the registration call. An
  injected retrieval-time `ESRCH` and other registration failures still surface
  as observer errors.
- Darwin-only tests inject all three paths and stress 100 fast real children per
  test invocation.

The fresh specification review identified one P1 in the first observer fix:

- When cancellation and the immediately ready exit notification coincided,
  either select path could consume the notification and return `cmd.Wait`
  without invoking process-group cleanup. A fast leader that left a same-group
  descendant could therefore let that descendant outlive canceled `exec`.
- `finishObservedCommand` now checks for cancellation before reaping. For a
  dedicated group, it sends the normal graceful/SIGKILL sequence and confirms
  quiescence while the unreaped leader still prevents process-group ID reuse.
  It then returns the leader's original wait result.
- `runExec` gives incomplete cleanup first priority, then preserves a real child
  exit status, and treats remaining cancellation errors as cancellation. The
  same race therefore returns the independently completed leader's status to
  the operator without weakening cleanup failures.
- The Darwin regression uses a real kqueue to wait for the helper leader to
  exit, substitutes registration-time `ESRCH`, cancels the context, and verifies
  user-visible status 37, a stopped heartbeat, and a quiescent process group. A
  separate injected test deterministically preserves exit status 37 without
  cancellation. The wait is bounded, and failure cleanup verifies the recorded
  PID still belongs to the expected group before signaling it.

## Verification evidence

The cancellation intersection was reproduced before its implementation fix:

```text
go test ./internal/app -run 'TestManagedCommand(PreservesExitStatusAfterDarwinRegistrationRace|CleansDescendantWhenDarwinRegistrationRaceIsCanceled)$' -count=1
FAIL: process group still has runnable descendants after canceled registration race
```

The first cleanup fix made that direct regression pass 100 repetitions. The
expanded end-to-end assertion then exposed the status-mapping layer:

```text
go test ./internal/app -run '^TestManagedCommandCleansDescendantWhenDarwinRegistrationRaceIsCanceled$' -count=1
FAIL: canceled registration race exit code = 130, want preserved child status 37
```

After the final fix, the frozen-tree release gate passed:

```text
make fmt-check
PASS (0.03s)

make vet
PASS (2.12s)

make test
PASS (4.60s)

make race
PASS (10.12s)

go test ./... -shuffle=on -count=20
PASS (72.22s)

go test ./internal/app -run 'TestManagedCommand(PreservesExitStatusAfterDarwinRegistrationRace|CleansDescendantWhenDarwinRegistrationRaceIsCanceled)$' -count=100
PASS (43.35s)

go test ./internal/app -run 'Test(ProcessExitNotificationAcceptsChildExitedBeforeRegistration|ProcessExitNotificationRejectsOtherRegistrationFailures|ProcessExitNotificationRejectsRetrievalNoSuchProcess|ManagedCommandPreservesExitStatusAfterDarwinRegistrationRace|ManagedCommandCleansDescendantWhenDarwinRegistrationRaceIsCanceled|ExecObserverFailureTerminatesChild|ExecCancellationTerminatesChildProcessGroup|ExecCancellationPreservesSignalCause|ExecCancellationKillsSameGroupDescendants)$' -count=50
PASS (101.63s)
```

The six monitored source/document hashes were identical before and after that
gate. All nine CGO-disabled CLI builds and Darwin amd64/arm64 plus Linux amd64
`internal/app` test-binary compiles also passed on the frozen tree. The targeted
Darwin race suite passed under the race detector 20 repetitions (9.875s).

Current working-tree checks after the Darwin observer fix:

```text
make fmt-check
make vet
make test
make race
PASS

go test ./... -shuffle=on -count=20
PASS

go test ./internal/app -run 'Test(ProcessExitNotification|ManagedCommandHandlesFastDarwinChildren)$' -count=100
PASS (10,000 real fast-child starts plus injected observer paths)

go test ./internal/app -run 'Test(ExecCredentialModesAndExitCode|ExecUsesFreshTokenHelperBlockerAndFailsClosedOnEntropyError|ExecNoticeNamesAmbientTransportWithoutLeakingValues)$' -count=500
PASS

go test ./internal/app -run 'Test(ExecObserverFailureTerminatesChild|ExecCancellationTerminatesChildProcessGroup|ExecCancellationPreservesSignalCause|CanceledMutationDoesNotCommit|CanceledActivationDoesNotEmitScript)$' -count=50
PASS

go test ./internal/app -run '^TestExecCancellationKillsSameGroupDescendants$' -count=300
PASS
```

All nine CGO-disabled CLI cross-builds passed, and Darwin amd64/arm64 plus
Linux amd64 `internal/app` test binaries compiled. A fresh isolated CLI stress
smoke ran 600 fast successful `exec` commands across all token modes with zero
observer/cleanup errors, preserved a child exit status of 37, verified guarded
credential clearing without canary leakage, and completed `doctor` with zero
errors plus the documented macOS ACL warning.

Current working-tree checks after the final Fish renderer edit:

```text
make fmt-check
make vet
make test
make race
PASS

go test ./... -shuffle=on -count=20
PASS

go test ./internal/contextenv -run 'Test(ShellInitRunsAtTopLevelInBashAndZsh|BashAndZshShellInitRejectNestedScopedActivation|BashAndZshShellInitRejectCrossDialectUse|BashShellInitRejectsReadonlyEnvironmentWithoutPartialActivation)$' -count=50
PASS

go test ./internal/config -run 'Test(PersistentLockRepairsRestrictiveUmask|StoreConcurrentUpdatesDoNotLoseMutations)$' -count=20
PASS

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o <temp>/contextenv.test ./internal/contextenv
<Ubuntu 24.04 / Fish 3.7.0> contextenv.test -test.run '^TestFishShellInitPropagatesNativeFailureAndHandlesMissingVariables$' -test.count=100
PASS

<Ubuntu 24.04 / Fish 3.7.0> contextenv.test
PASS

CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o <temp>/vaultctx-<os>-<arch> ./cmd/vaultctx
PASS for Linux amd64/arm64, Windows amd64, FreeBSD amd64, OpenBSD amd64,
illumos amd64, Plan 9 amd64, and Darwin amd64/arm64
```

A fresh isolated CLI smoke also passed with an absolute temporary config and a
new temporary binary. It covered version, add/use/current, Fish rendering,
fingerprinting, destination/fingerprint-bound `exec`, ambient credential
clearing with fake canaries, and `doctor`. Standard output remained
machine-safe, no canary values appeared in diagnostics, and `doctor` reported
zero errors plus only the documented macOS extended-ACL warning.

The earlier Fish-only checkpoint included 50 cancellation/observer repetitions
and 300 same-group descendant-cleanup repetitions. Both suites were rerun after
the Darwin observer edit; the current evidence above supersedes that checkpoint.

Current-tree checks completed after the `EPERM` fix:

```text
go test ./internal/app -run 'Test(ProcessGroupActivePreservesPermissionDenied|LinuxProcessGroupState|ExecCancellationKillsSameGroupDescendants)$' -count=10
PASS

make fmt-check
make vet
make test
make race
PASS
```

Current-tree checks completed after the two P2 fixes:

```text
gofmt -l cmd internal
PASS (no files reported)

go test ./...
PASS

go test ./internal/app -run 'Test(ExecCancellationKillsSameGroupDescendants|ConfigValidationStatusDoesNotOverclaimUnsupportedPlatforms|CanceledActivationDoesNotEmitScript)$' -count=10
PASS

go test ./internal/app -run '^TestExecCancellationKillsSameGroupDescendants$' -count=300
PASS (124.951s)

go test ./internal/app -run 'Test(ExecCancellationKillsSameGroupDescendants|LinuxProcessGroupState)$' -count=20
PASS

make fmt-check
PASS

make vet
PASS

make test
PASS

make race
PASS

go test ./... -shuffle=on -count=10
PASS

CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o /private/tmp/vaultctx-<os>-<arch> ./cmd/vaultctx
PASS for Linux amd64/arm64, Windows amd64, FreeBSD amd64, OpenBSD amd64,
illumos amd64, Plan 9 amd64, and Darwin amd64/arm64
```

The first post-review complete gate exposed the Linux zombie-only group failure
during `make test`; that attempt did not reach `make race`. The required full
gate and the formerly stale focused/smoke gates are now green. Fish and
PowerShell are not installed directly on this host. Fish 3.7 was exercised in
the disposable Linux container described above, and both Fish and PowerShell
passed in PR #2's `shell-integration` jobs.

## Remaining release checklist

If source changes again, rerun the complete release gate with sandbox-writable,
task-specific caches before relying on any evidence above. Otherwise, the next
agent should:

1. Commit the cancellation-intersection fix and obtain fresh independent
   Standards and Specification reviews on that fixed point.
2. Push the update to PR #2 and require the complete CI workflow to pass on the
   new head. The earlier Fish/PowerShell jobs are green but predate this fix.
3. Add `docs/review-report.md` with exact current-tree review, local, and CI
   evidence plus caveats.
4. Build a fresh `v0.1.0` candidate and checksum only after the last source
   change. Do not overwrite or ship the stale `bin/vaultctx` beforehand.
5. Ask the owner to choose a license before any public release or external
   contribution workflow. Do not infer that legal choice.

## Environment and release caveats

- This is an active Git repository. `origin/main` was `94cf22d` when
  `codex/fix-fish-shell-integration` was created. `origin` points to
  `git@github.com:sohilkaushal/vaultctx.git`. This handoff does not represent a
  release tag or published GitHub release.
- The repository intentionally has no `LICENSE`. A local/internal candidate can
  be tested, but public publication and external contributions require the
  owner to choose a license.
- The local host is `darwin/arm64` with Go 1.26.6. Vault and `fzf` are installed;
  Bash and Zsh are available; Fish and PowerShell are not.
- `README.md` and `SECURITY.md` describe this as an MVP, not a production audit
  or an authorization boundary.

## Review history

- Implementation/security reviewer: issued `SHIP` with no P1/P2 findings on
  the tree before the latest two fixes. That stale verdict is superseded by the
  fresh reviews below.
- Final-quality reviewer: issued `DO NOT SHIP` for the process-group and doctor
  portability P2s above. Both are merged and locally verified.
- Fresh standards and specification reviewers inspected source commit
  `6ba6285` against merged `main`. They found no P1/P2 implementation issue,
  no scope creep, and no lifecycle/doctor regression, and returned `SHIP` for
  the change to proceed to PR CI. The standards reviewer found one P3 stale
  checklist line in this document; the current docs-only follow-up removes it.
- Those reviews predate the Darwin observer edit discovered by PR #2 and do not
  approve that new stop-ship lifecycle change. A fresh review is required.
- Standards review of `b0dbb69` returned `SHIP` with no P1/P2 finding. It found
  P3 gaps in deterministic exit-status coverage and this handoff's stale
  checklist wording; both are addressed in the current working tree.
- Specification review of `b0dbb69` returned `DO NOT SHIP` for the P1
  cancellation/ready-exit cleanup bypass described above. The deterministic
  regression first failed and now passes.
- A targeted post-fix reviewer found no P0-P3 issue and returned `SHIP` after
  verifying signal-before-reap ordering, consumed-notification handling,
  user-visible exit status, bounded/PID-safe tests, and architecture accuracy.
  The full post-edit gate is green. Independent final Standards and
  Specification reviews remain required on the committed fixed point.
- A separate release reading remains `DO NOT SHIP` for public publication until
  mandatory Fish/PowerShell CI, the review report, fresh candidate, and license
  decision are complete. That is a release-state gate, not a code finding.

No agent should collapse this history into an unconditional release approval
until the remaining release checklist is complete.
