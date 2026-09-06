# Preparing v0.1.0

This runbook prepares the first macOS/Linux MVP release. Packaging does not
publish anything. The current branch contains documentation only; no release
archives or new runtime verification results are claimed.

## Decisions and publication gates

- [ ] Owner chooses a license; add its complete text as `LICENSE` and update
  the no-license statements in `README.md`, `AGENTS.md`, and `docs/handoff.md`.
- [ ] Owner supplies a private security contact. Enable and verify GitHub
  private vulnerability reporting and replace the setup instruction in
  `SECURITY.md` with the actual reporting route. Do not claim settings were
  enabled without checking them.
- [ ] Review and merge the final release documentation. Update supported
  status in `SECURITY.md` to the intended MVP support policy.
- [ ] All P1/P2 findings are resolved, with independent review restamps after
  the last stop-ship edit. Preserve the existing review evidence; documentation
  updates do not constitute a new source review.
- [ ] Run `make fmt-check vet test race`, `go test -shuffle=on -count=20 ./...`,
  the relevant lifecycle repetitions, and the nine CGO-disabled cross-builds
  recorded in [review-report.md](review-report.md). Record the final commit,
  toolchain, commands, results, and any justified reuse of unchanged-source
  evidence. Cross-compilation alone is not a runtime test.
- [ ] Confirm CI for the final release commit, including real Fish/PowerShell,
  Linux/macOS stable/oldstable, race, and CodeQL. Record exact run links.
- [ ] Build after the final source change; smoke-test native macOS and Linux
  candidates with disposable config and fake destinations. Include version,
  help, add/list, activation/token clearing, successful exec, and SIGTERM 143.
- [ ] Record artifact checksums and build provenance, finalize
  [the release notes](releases/v0.1.0.md), and obtain publication approval.

## Package from a clean, reviewed checkout

Prerequisites: Bash, Git, Go 1.25+, `tar`, and either `sha256sum` or `shasum`.
The script below is a maintainer recipe; it does not run the verification gates
or establish license approval. Run it only after the checks above pass and the
owner-selected license and final documentation have been committed. It writes
to a fresh temporary directory outside the checkout.

```bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
test -z "$(git status --porcelain)"
git ls-files --error-unmatch LICENSE >/dev/null
test -s LICENSE

release_version=v0.1.0
release_commit=$(git rev-parse HEAD)
release_dir=$(mktemp -d "${TMPDIR:-/tmp}/vaultctx-release.XXXXXX")

if command -v sha256sum >/dev/null 2>&1; then
  release_hash=(sha256sum)
else
  command -v shasum >/dev/null
  release_hash=(shasum -a 256)
fi

printf 'version=%s\ncommit=%s\n' "$release_version" "$release_commit" > "$release_dir/BUILDINFO.txt"
go version >> "$release_dir/BUILDINFO.txt"

for release_os in darwin linux; do
  for release_arch in amd64 arm64; do
    release_name="vaultctx_${release_version}_${release_os}_${release_arch}"
    release_stage="$release_dir/$release_name"
    mkdir "$release_stage"
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" \
      go build -buildvcs=true -trimpath \
      -ldflags "-s -w -X main.version=$release_version" \
      -o "$release_stage/vaultctx" ./cmd/vaultctx
    cp README.md SECURITY.md LICENSE "$release_stage/"
    cp docs/releases/v0.1.0.md "$release_stage/RELEASE-NOTES.md"
    go version -m "$release_stage/vaultctx" > "$release_stage/BUILDINFO.txt"
    cat "$release_stage/BUILDINFO.txt" >> "$release_dir/BUILDINFO.txt"
    tar -czf "$release_dir/$release_name.tar.gz" -C "$release_dir" "$release_name"
  done
done

(
  cd "$release_dir"
  "${release_hash[@]}" ./*.tar.gz BUILDINFO.txt > SHA256SUMS
)
printf 'Prepared archives in %s\n' "$release_dir"
```

Inspect each embedded build record: `vcs.revision` must match the selected
commit, `vcs.modified` must be `false`, and the OS/architecture must match its
archive name. Inspect archive contents and run the native smoke checks before
uploading. Do not reuse the earlier internal candidate's checksum. These
archives are unsigned; deterministic archive bytes and signing are not claimed.

Verify checksums from the artifact directory with `sha256sum -c SHA256SUMS`
(Linux) or `shasum -a 256 -c SHA256SUMS` (macOS).

## Publish only after approval

1. Confirm the approved release commit and that `v0.1.0` is unused. Do not move
   an existing tag to another commit.
2. Create the version tag at that exact commit and a draft GitHub release.
3. Attach the four `.tar.gz` files, `SHA256SUMS`, and `BUILDINFO.txt`; paste the
   finalized release notes. Keep the draft unpublished until its assets and
   links have been checked.
4. Publish with the owner's approval, then record the release URL, tag, commit,
   artifact checksums, CI links, and support status in the handoff.

Windows ACL/process hardening, completions, rename, online diagnostics, keychain
storage, and team bundles remain future work; they are not silently included in
the MVP release scope.
