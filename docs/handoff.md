# Current handoff

Last updated: 2026-09-02 (Australia/Melbourne)

## Current outcome

The security-focused MVP is feature-complete, but it is **not yet
release-stamped**. Two final-quality P2 findings were addressed in the current
tree. Their focused regressions pass, including the original flaky macOS test
300 consecutive times. The full post-fix release matrix and independent
reviewer restamp are still pending.

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
  killed process group no longer exists. It sends no signal after reaping the
  leader, avoiding damage if the numeric group ID is reused.
- If group quiescence cannot be confirmed, the code returns an error wrapping
  `errProcessCleanupIncomplete`; `runExec` no longer masks that error as ordinary
  context cancellation.
- `TestExecCancellationKillsSameGroupDescendants` now also asserts that the
  process group is absent when `vaultctx` returns.
- `doctor` reports verified owner/POSIX checks only on Linux, retains the macOS
  extended-ACL warning, and warns that filesystem ownership/hard-link checks
  are unverified on other platforms.
- `TestConfigValidationStatusDoesNotOverclaimUnsupportedPlatforms` covers
  Linux, macOS, Windows, FreeBSD, OpenBSD, and Plan 9 messages.

## Verification evidence

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
```

These remaining gates were green before the two latest P2 fixes, but are stale
and must be rerun on the current tree:

- `go test -race ./...`
- `go vet ./...`
- `go test ./... -shuffle=on -count=10`
- focused Bash/Zsh, cancellation, and lock repetitions
- CGO-disabled builds for Linux amd64/arm64, Windows amd64, FreeBSD amd64,
  OpenBSD amd64, illumos amd64, and Plan 9 amd64
- a real CLI add/use/env/exec/doctor smoke flow

Fish and PowerShell are not installed on this host. Their real-shell tests are
present in `internal/contextenv/contextenv_test.go`; CI installs Fish, requires
`pwsh`, and runs both suites. Treat that CI job as mandatory before public
release.

## Resume checklist

Use a sandbox-writable cache:

```sh
export GOCACHE=/private/tmp/vaultctx-handoff-gocache
```

Then run:

```sh
gofmt -l cmd internal
go vet ./...
go test ./...
go test -race ./...
go test ./... -shuffle=on -count=20
go test ./internal/contextenv -run 'Test(ShellInitRunsAtTopLevelInBashAndZsh|BashAndZshShellInitRejectNestedScopedActivation|BashAndZshShellInitRejectCrossDialectUse|BashShellInitRejectsReadonlyEnvironmentWithoutPartialActivation)$' -count=50
go test ./internal/config -run 'Test(PersistentLockRepairsRestrictiveUmask|StoreConcurrentUpdatesDoNotLoseMutations)$' -count=20
```

Cross-build the entrypoint with `CGO_ENABLED=0` for:

```text
linux/amd64
linux/arm64
windows/amd64
freebsd/amd64
openbsd/amd64
illumos/amd64
plan9/amd64
```

Write outputs outside the repository, for example under `/private/tmp`, so
targets cannot overwrite one another or the local binary.

After all gates pass:

1. Obtain fresh independent `SHIP` verdicts on the current tree, specifically
   asking reviewers to recheck process-group quiescence and doctor portability.
2. Run the Fish/PowerShell CI integration job.
3. Add `docs/review-report.md` with exact current-tree evidence and caveats.
4. Build a fresh `v0.1.0` binary and checksum it.
5. Smoke-test add/list/saved-default use, shell rendering, guarded `exec`, and
   `doctor` with an isolated absolute `VAULTCTX_CONFIG`.

## Environment and release caveats

- This is an active Git repository. Work started from `main` at `43eab54`, with
  `origin` pointing to `git@github.com:sohilkaushal/vaultctx.git`. Verify the
  current branch and remote state when resuming; this handoff does not represent
  a release tag or published GitHub release.
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
  portability P2s above. Both are addressed locally, but the reviewer has not
  yet restamped the fix.

No agent should collapse this history into an unconditional release approval
until the resume checklist is complete.
