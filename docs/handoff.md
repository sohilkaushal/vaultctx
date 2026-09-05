# Session handoff

Last updated: 2026-09-05. Continue on `codex/fix-fish-shell-integration`.
The documented macOS EPERM failure and subsequent review findings are fixed.

## Current checkpoint

- Reviewed source: `5c6c3d4df47d02c0cc88934315a795f571df1635`.
- Verified base: `94cf22d5c92f9a60a683e62be8acbf2da2910abc` (`origin/main`).
- Remote: `git@github.com:sohilkaushal/vaultctx.git`.
- [PR #2](https://github.com/sohilkaushal/vaultctx/pull/2) is open. The reviewed
  source is pushed; its title/body now cover Fish and process cleanup.
- Independent Standards/security and Specification reviews both restamped
  `5c6c3d4` with no unresolved P1/P2 findings.
- Full local fmt/vet/test/race gates and focused Linux regressions pass.
  Final repetitions and CI are being collected. See
  [review-report.md](review-report.md) for exact commands and evidence.

## Root cause and final behavior

An unsandboxed temporary `ps` diagnostic showed the child in zombie state
before Darwin's final group-SIGKILL returned EPERM. Apple's `killpg1` skips
zombies during group signaling. The diagnostic was removed.

The Darwin EPERM exception requires successful direct-child cleanup, reaping,
and independent group-quiescence confirmation. EPERM from a group probe still
means potentially active on macOS and Linux. Live descendants, failed probes,
and other signal errors remain cleanup errors.

Pending observations precede `Wait` in group and terminal-child cancellation,
preventing Linux `waitid(WNOWAIT)` from racing with reaping. If direct SIGKILL
is refused, exit waiting is bounded to one second; group probing has a separate
one-second bound. A background waiter retains eventual reaping responsibility
and sends no more signals. Observer completion is published separately so an
available retrieval error survives a reap timeout. Failed cleanup cannot
promise that a signal-denied child has stopped.

Cancellation statuses 130/143, independently completed status 37, observer
diagnostics, signal-before-reap ordering, and descendant cleanup have
adversarial regressions. The branch retains the Fish 3.7 empty-value and
option-terminator fix and Darwin kqueue registration-race fix.

## Review history

| Checkpoint | Result |
| --- | --- |
| `c505489` | Saved WIP; ordinary cancellation fails with EPERM. |
| `9b7c3ff` | EPERM fixed; both reviewers found P2 terminal observer ordering and indefinite double-refusal cleanup. |
| `e9dcf6d` | Original P2s fixed; Standards passed, Specification found pending observer diagnostic lost on timeout. |
| `5c6c3d4` | Final diagnostic fix; both reviewers pass, no unresolved P1/P2 findings. |

## Remaining completion work

CI follow-up: on source head `5c6c3d4`, the push CI, all macOS jobs, both
Fish/PowerShell jobs, and CodeQL passed. The PR Ubuntu/oldstable job failed two
new live-heartbeat assertions that assumed progress within 50ms. The matching
push job passed. The tests now require actual heartbeat advancement within a
bounded two-second polling window; runtime code is unchanged. Fresh test gates,
test-only review restamps, and CI are pending for this adjustment.
The final `5c6c3d4` shuffle also exposed a test-cleanup race: killing the
intentionally surviving descendant without confirming quiescence let TempDir
removal race with its final heartbeat write. That cleanup now confirms group
quiescence before returning. Neither adjustment changes runtime source.

1. Collect final shuffled, repeated lifecycle/race, Linux, and CI results for
   `5c6c3d4`. CI must include real Fish/PowerShell, macOS/Linux stable and
   oldstable Go, race, and CodeQL.
2. Build and smoke-test a fresh internal candidate after gates are green;
   record source commit, version, and checksum. The old candidate was stale.
3. Recheck GitHub Codex: the last observed summary covers `adda5bd`, not the
   new source. No new review request comment has been sent this session.
4. Commit/push final evidence documents. Any later stop-ship source edit needs
   fresh gates and independent restamps.
5. Public release remains blocked by the absent license (owner decision) and
   private-reporting setup in `SECURITY.md`. No merge, tag, license, or public
   release artifact has been created.

## Tooling

Host: macOS arm64, Go 1.26.6, Bash/Zsh installed. Task caches:
`GOCACHE=/private/tmp/vaultctx-eperm-gocache` and
`GOMODCACHE=/private/tmp/vaultctx-eperm-gomodcache`.

The existing Ubuntu 24.04 arm64 Podman container `vaultctx-fish37-debug`
was reused for compiled Linux app tests. It has Fish but no Go compiler; test
binaries are copied into `/tmp`. No unrelated containers were changed.
Cross-build artifacts: `/private/tmp/vaultctx-eperm-cross`. Runtime remains
standard-library-only, with no license and no `go.sum`.
