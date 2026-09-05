# Architecture

## Boundary

`vaultctx` is a local orchestration CLI. It does not implement HashiCorp Vault's
server plugin protocol and it does not link the Vault API. Delegating remote
operations to the official `vault` binary keeps selection network-free and
avoids duplicating authentication behavior.

```text
strict config ──> validated Context ──> environment plan
                                          │
                      ┌───────────────────┼──────────────────┐
                      v                   v                  v
                 shell renderer       exec child        JSON output

strict config ──> metadata records ──> isolated fzf / numbered picker
```

## Packages

- `internal/config`: versioned model, strict parsing, validation, process lock,
  and same-directory atomic replacement.
- `internal/contextenv`: the pure mapping from a context to set/unset operations,
  child environments, and shell-specific quoting.
- `internal/selector`: passive selection. It never receives credentials, never
  calls a shell, and never contacts Vault.
- `internal/app`: CLI orchestration, context lifecycle, diagnostics, and direct
  child-process execution.
- `cmd/vaultctx`: platform wiring, signal cancellation, and exit status.

## Destination and credentials

Friendly context names are not a credential boundary. An effective destination
is the address, namespace, TLS server name, CA/client-certificate references,
agent address, and proxy address together.

The MVP has three `exec` modes:

1. **session sources blocked** (default): remove `VAULT_TOKEN`, MFA, and custom
   headers, then set a fresh random non-secret token sentinel so the Vault CLI
   cannot fall through to a global token-helper lookup;
2. **token-helper lookup**: explicitly allow the Vault CLI's configured helper
   to supply a token; or
3. **ambient token**: explicitly forward a non-empty `VAULT_TOKEN` for one child.

All modes print the effective destination and auth choice before execution.
Automation can bind both the friendly name and a stable SHA-256 digest of the
stored connection metadata, so editing a named context fails closed. TLS fields
contribute their path strings; certificate and key file contents are never read
for fingerprinting.

The default does not disable context-bound mTLS or Vault Agent auto-auth. Those
are explicit connection-profile mechanisms and remain visible in the context
and pre-exec notice. Nor is `exec` a general credential sandbox: an explicit
login/auth command can consume inherited auth-method or cloud-provider inputs.
There is no stable finite list across Vault plugins, so the MVP documents this
boundary instead of claiming incomplete sanitization.

The sentinel affects lookup fallback only. It cannot prevent successful Vault
login commands from writing a returned token through the configured helper;
operators who do not want persistence must use Vault's `login -no-store` mode.

Every connection, transport, request-wrapping, and credential environment
constant used by the Vault API is either replaced from the selected context or
cleared (`VAULT_TOKEN` is the sole explicit forwarding exception). General
proxy/trust variables and `GODEBUG` remain parent-owned because clearing them
could break unrelated runtime policy; their names are warned without exposing
their values.

Shell activation clears the ambient token but cannot disable the default token
helper without breaking later login behavior. It therefore warns when a global
token file exists and recommends an address/namespace-aware helper.

Bash and Zsh wrappers reject nested function calls because dynamically scoped
locals can otherwise make an outer credential resurface after a global address
change. Fish instead updates both the caller-visible binding and a session
global shadow; empty shadows preserve any universal value without exposing it
to Vault. PowerShell environment variables are process-scoped. Raw POSIX plans
remain available for explicit top-level evaluation, but the generic `sh`
wrapper is intentionally not offered.

## State

The config's `current` and `previous` fields provide a default for non-shell
usage. A `vctx` activation does not modify that saved default. Once shell
integration is active, `VAULTCTX_CONTEXT` and
`VAULTCTX_PREVIOUS` take precedence, preventing two terminals from continually
overwriting one another's active selection. Current and previous fingerprint
variables detect a named context replaced after activation. Persisted previous
state is fingerprint-bound too. `-` resolves the previous context.

## Filesystem durability

On macOS/Linux, mutations take an OS-released exclusive advisory lock, validate
the latest config, write a size-bounded `0600` temporary file in the same
directory, flush it, rename it over the destination, and sync the directory.
Readers reject oversized, non-regular, symlinked, permission-unsafe,
non-canonical/unknown-field, duplicate-key, and structurally invalid data.
Other platforms use a create-exclusive sentinel lock; a crash can leave that
exact lock file behind. macOS UID/POSIX-mode checks do not inspect extended ACLs.

The remaining POSIX hardening item is to anchor all operations to an opened
directory descriptor using no-follow `openat`/`renameat` operations. The current
implementation performs before/after identity checks when opening the final
file, but a privileged same-user attacker remains outside the MVP boundary.

## Selector isolation

Candidates are NUL-delimited records of context name, current marker, address,
and namespace. `fzf` is resolved to an absolute path and receives a small
allowlist of locale/terminal variables. User `FZF_DEFAULT_*` hooks and all Vault
variables are absent. Fixed arguments disable multiple selection; output is
bounded and must resolve to a known in-memory name.

## Child process lifecycle

macOS/Linux non-interactive children run in a dedicated process group. The
entry point retains SIGINT versus SIGTERM as the cancellation cause; the group
receives that signal, gets a bounded grace period, and then receives SIGKILL.
Signal-derived child exits use the conventional `128 + signal` status. Linux
uses `/proc` process states when confirming cleanup because an orphaned,
already-killed zombie can keep `kill(-pgid, 0)` successful until the host's PID
1 reaps it; zombie-only groups cannot execute and are quiescent. A child with
terminal stdin remains in the foreground process group so interactive prompts
work. macOS observes child exit with a one-shot kqueue process filter. If a
fast child exits between `Start` and filter registration, registration can
return `ESRCH`; because the direct child is still unreaped, its PID cannot have
been reused, so the caller proceeds to `Wait` for the real status. Other
registration errors and every retrieval error remain fail-closed. If context
cancellation has already arrived when an exit notification is consumed, the
unreaped leader continues to reserve the process-group ID while vaultctx
terminates and confirms the group; the leader's real wait result is retained.
Registration and retrieval failures take the same signal-before-reap path,
confirm group quiescence, and retain the observer diagnostic. Pending exit
notifications are consumed before reaping, so Linux's `waitid(WNOWAIT)` observer
cannot race with `Wait` and spuriously report `ECHILD`. A failed final group
signal, unexpected reap result, failed probe, or quiescence timeout is reported
as incomplete cleanup rather than being masked by concurrent cancellation.
The sole additional signal-error exception is macOS group-SIGKILL `EPERM`:
Darwin skips zombies during group signaling, so an unreaped, exited leader
alone can cause that result. It is accepted only after the direct-child
fallback and reap succeed and a separate probe confirms group quiescence.
An `EPERM` probe still means potentially active on both primary platforms;
neither a failed probe nor a live descendant is excused by this exception.
The observer-before-reap ordering also applies to terminal-attached children.
If direct-child SIGKILL is refused, waiting for exit is limited to one second
before reporting incomplete cleanup; group probing has its own one-second
bound. A background waiter retains responsibility for the child's eventual
reap and never sends another signal. This error path cannot promise that a
signal-denied child has stopped.
On other platforms cancellation is direct-child-only, and a deliberately
daemonized process is outside the portable cleanup policy.

## Future design constraints

- A token helper must use an OS keychain and bind tokens to canonical
  destination metadata—not merely an address or display name.
- Connectivity checks must be explicit, bounded, unauthenticated by default,
  and understand Vault's documented status exit codes.
- Shared contexts require provenance, conflict rules, and signature validation.
- Production labels can reduce mistakes but must never be presented as an
  authorization control.
