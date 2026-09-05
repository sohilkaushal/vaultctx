# Current handoff

Last updated: 2026-09-05 (UTC)

## Current outcome

The security-focused MVP is feature-complete, but it is **not yet
release-stamped**. PR #1 merged the agent handoff and Linux process-lifecycle
fixes to `main` as `94cf22d`. Its Go, race, macOS/Linux, and CodeQL checks
passed, but both mandatory `shell-integration` runs failed on Fish 3.7.0.

Work is now on `codex/fix-fish-shell-integration`. The Fish activation guard no
longer relies on an empty command substitution that Fish 3.7 drops, and it uses
an explicit option terminator so a leading-hyphen value cannot be interpreted
as a `string join` flag. The exact Fish 3.7 failure has been reproduced locally
in an Ubuntu 24.04 container and the corrected real-shell suite passes. The
post-edit local full/race/shuffled gates, cross-build matrix, and isolated CLI
smoke are green. Fresh independent review and a green follow-up PR CI run are
still required.

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

## Verification evidence

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

The cancellation/observer suite passed 50 repetitions and same-group
descendant cleanup passed 300 repetitions immediately before the final
Fish-only edit. Process lifecycle code and tests did not change afterward; the
complete test/race/shuffled gates were rerun after the Fish edit.

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
the disposable Linux container described above; PowerShell must still pass in
the follow-up PR's mandatory `shell-integration` job.

## Remaining release checklist

If source changes again, rerun the complete release gate with sandbox-writable,
task-specific caches before relying on any evidence above. Otherwise, the next
agent should:

1. Commit the Fish fix and this handoff update.
2. Obtain fresh independent `SHIP` verdicts on the committed tree, asking the
   reviewers to recheck the Fish renderer as well as process-group quiescence
   and doctor portability.
3. Push the branch, open the follow-up PR, and require the complete CI workflow,
   especially Fish and PowerShell `shell-integration`, to pass.
4. Add `docs/review-report.md` with exact current-tree review, local, and CI
   evidence plus caveats.
5. Build a fresh `v0.1.0` candidate and checksum only after the last source
   change. Do not overwrite or ship the stale `bin/vaultctx` beforehand.
6. Ask the owner to choose a license before any public release or external
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
  the tree before the latest two fixes. That verdict is stale and must be
  renewed.
- Final-quality reviewer: issued `DO NOT SHIP` for the process-group and doctor
  portability P2s above. Both are merged and locally verified, but a current
  reviewer has not yet restamped the complete Fish-fix tree.

No agent should collapse this history into an unconditional release approval
until the remaining release checklist is complete.
