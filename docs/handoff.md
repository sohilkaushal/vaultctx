# Session handoff

Last updated: 2026-09-06. Continue on `codex/fix-fish-shell-integration`.
The documented macOS EPERM failure and subsequent review findings are fixed.

## Current checkpoint

- Reviewed/tested checkpoint: `5f92942a4f79d206bb55e15bf1a45e7eb76a13f7`.
  Runtime code is unchanged from `5c6c3d4`.
- Verified base: `94cf22d5c92f9a60a683e62be8acbf2da2910abc` (`origin/main`).
- Remote: `git@github.com:sohilkaushal/vaultctx.git`.
- [PR #2](https://github.com/sohilkaushal/vaultctx/pull/2) remains open. The
  reviewed checkpoint is pushed; title/body cover Fish and process cleanup.
- Independent Standards/security and Specification reviewers both restamped
  `5f92942` with no unresolved P1/P2 findings.
- Full fmt/vet/test/race, shuffled x20, Linux lifecycle x20, adversarial race
  x10, and cross-build gates pass. Both final CI runs pass, including real
  Fish/PowerShell, macOS/Linux stable/oldstable, race, and CodeQL.
- Fresh internal `v0.1.0` candidate: `bin/vaultctx`, built from clean
  `5f92942` after local gates passed; smoke checks pass.
  SHA-256: `cd1561d8191bf2cb6d05055d5013f7d409cca551a886b133f486f27bf2d7f748`.
- Exact commands, reviews, CI links, and candidate provenance:
  [review-report.md](review-report.md). Evidence-only commits after this
  checkpoint do not change its reviewed runtime or tests.

## Root cause and final behavior

A temporary unsandboxed `ps` diagnostic confirmed a zombie leader before
Darwin's group-SIGKILL returned EPERM. Apple's `killpg1` excludes zombies.
The diagnostic was removed. Darwin's EPERM exception now requires successful
direct-child cleanup, reaping, and independent group-quiescence confirmation.
EPERM probes remain potentially active on macOS and Linux. Live descendants,
failed probes, and other signal errors remain cleanup errors.

Pending observations precede `Wait` in group and terminal-child cancellation,
avoiding Linux `waitid(WNOWAIT)`/reap races. Refused direct SIGKILL bounds exit
waiting to one second, with a separate one-second group-probe bound. A sole
background waiter retains eventual reaping responsibility without more signals.
Observer results are published separately so available retrieval diagnostics
survive a reap timeout. Failed cleanup cannot promise a signal-denied child
has stopped.

Regressions cover 130/143 cancellation, independently completed status 37,
observer diagnostics, signal-before-reap ordering, and descendant cleanup.
The branch retains Fish 3.7 empty-value/option-terminator and Darwin kqueue
registration-race fixes.

## Review and verification history

| Checkpoint | Result |
| --- | --- |
| `c505489` | Saved WIP; ordinary cancellation fails with EPERM. |
| `9b7c3ff` | EPERM fixed; both reviewers found terminal observer ordering and unbounded double-refusal cleanup P2s. |
| `e9dcf6d` | Those P2s fixed; Specification found pending observer diagnostics lost on timeout. |
| `5c6c3d4` | Runtime approved by both reviewers. CI/shuffle exposed test-only heartbeat timing and cleanup races. |
| `5f92942` | Test fixes restamped by both reviewers; final local, Linux, and CI gates all pass. |

The tests now poll for actual heartbeat advancement within two seconds instead
of assuming scheduling within 50ms. Cleanup confirms the intentionally surviving
descendant's quiescence before removing its temporary directory.

## Remaining owner/release decisions

- The GitHub Codex summary was rechecked after the final push and still covers
  `adda5bd`. Do not represent it as approval of the new head. No new review
  request comment was sent. Local independent restamps are recorded separately.
- Public release remains blocked by the absent license (owner decision) and
  private-reporting setup in `SECURITY.md`. No merge, tag, license, or public
  release artifact has been created. The internal candidate is not a release.
- Any later stop-ship edit requires fresh gates and independent restamps.

## Tooling

Host: macOS arm64, Go 1.26.6, Bash/Zsh installed. Task caches:
`GOCACHE=/private/tmp/vaultctx-eperm-gocache` and
`GOMODCACHE=/private/tmp/vaultctx-eperm-gomodcache`.

The existing Ubuntu 24.04 arm64 container `vaultctx-fish37-debug` was reused
for compiled Linux app tests; it has Fish but no Go compiler. Test artifacts
are under `/private/tmp/vaultctx-eperm-cross` and the container's `/tmp`.
No unrelated containers were changed.

SSH push later failed because the agent could not sign. The existing
authenticated HTTPS connection succeeded without changing the configured remote:

```sh
git -c credential.helper='!gh auth git-credential' push https://github.com/sohilkaushal/vaultctx.git HEAD:refs/heads/codex/fix-fish-shell-integration
```

The project remains standard-library-only, with no license and no `go.sum`.
