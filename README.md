# vaultctx

`vaultctx` is a local, fuzzy Vault context switcher for administrators. It
manages connection metadata, integrates with `fzf`, and applies an explicit set
of `VAULT_*` variables. Its schema has no token, password, MFA, secret-ID,
unseal-key, or license fields, and environment import uses an explicit
connection-metadata allowlist. Free-form metadata is stored verbatim, so never
put credentials or other secrets in a context name, namespace, or description.

This is a workstation CLI companion, **not** a Vault server plugin. Vault
plugins implement auth, secrets, or database backends inside Vault's lifecycle;
context selection belongs at the administrator's shell.

> Status: security-focused MVP. The primary targets are macOS and Linux. The
> PowerShell environment renderer is included, but Windows filesystem hardening
> and CI are still roadmap items. On other platforms, an interrupted writer can
> leave `config.json.lock`; after confirming no writer is active, remove that
> exact file manually. macOS/Linux use crash-released OS advisory locks.

## Why another context tool?

There are already capable projects in this space, including HashiCorp's
[Target CLI](https://www.hashicorp.com/de/blog/target-cli-the-context-switcher-for-hashicorp-tools)
and the community [vaultie](https://github.com/pacerino/vaultie). Use those if
their profile and authentication model fits your environment.

`vaultctx` deliberately occupies a narrower administrator-safety niche:

- strict, versioned context data with no credential fields;
- deterministic clearing of stale Vault connection variables;
- passive `fzf` selection with no previews or network requests;
- ambient `VAULT_TOKEN`/MFA/custom headers and global-helper fallback blocked
  for `exec` unless an operator opts in explicitly;
- per-shell current/previous context tracking; and
- auditable JSON, atomic updates, local diagnostics, and adversarial tests.

## Build and try it

You need Go 1.25 or newer to build. The official `vault` CLI is required for
normal use and `fzf` is optional; without `fzf`, an interactive numbered picker
is used. The hardened filesystem and process-group paths target macOS and Linux.

```sh
make check
make build
export PATH="$PWD/bin:$PATH"
```

Release builds can embed a version with `make build VERSION=v0.1.0`.

For a persistent per-user installation, copy the binary into a directory that
is already on your `PATH`, for example:

```sh
mkdir -p "$HOME/.local/bin"
install -m 0755 bin/vaultctx "$HOME/.local/bin/vaultctx"
```

Add contexts. A plaintext Vault or Vault Agent URL is accepted only for
`localhost` or a literal loopback address and requires `--allow-http`. An HTTP
Vault proxy also requires that acknowledgement, and it cannot be combined with
a plaintext Vault or Agent destination; endpoint TLS must remain enabled.

```sh
vaultctx add dev \
  --address http://127.0.0.1:8200 \
  --allow-http \
  --description "Local development"

vaultctx add prod-admin \
  --address https://vault.example.com:8200 \
  --namespace admin \
  --ca-cert /absolute/path/company-ca.pem \
  --description "Primary production administrator namespace"

vaultctx list
vaultctx doctor
```

## Shell integration

A child process cannot change its parent shell. Install the small `vctx`
function once in your shell startup file:

```sh
# zsh
eval "$(vaultctx shell-init zsh)"
```

```sh
# bash
eval "$(vaultctx shell-init bash)"
```

```fish
# fish
vaultctx shell-init fish | source
```

Then use an exact name, open the interactive picker, or return to the previous
context:

```sh
vctx prod-admin
vctx
vctx -
```

Running bare `vaultctx NAME` or `vaultctx use NAME` only saves the default for
future non-shell commands; it prints that the current shell is unchanged and
warns when active Vault variables remain. Use `vctx` to change the current
shell, or `vaultctx exec` for one bounded command. Conversely, `vctx` changes
only the current shell and does not overwrite the saved default.

The installed Bash/Zsh wrapper refuses calls from inside another function:
dynamic local environment bindings can otherwise resurface an outer token under
a newly global address. Call `vctx` at the interactive top level; use
`vaultctx exec` in scripts and functions. `shell-init` is intentionally limited
to Bash, Zsh, Fish, and PowerShell. A raw `env --shell sh` plan is for explicit
top-level evaluation only.

Activation sets `VAULTCTX_CONTEXT`, `VAULTCTX_PREVIOUS`, and identity
fingerprints for both per shell. If either named context is later replaced,
implicit current/previous resolution fails closed until that shell explicitly
selects a context again. Activation clears
the following before applying the selected context, so namespace, proxy, or TLS
settings cannot bleed over from the previous target:

```text
VAULT_ADDR            VAULT_AGENT_ADDR       VAULT_CACERT
VAULT_CACERT_BYTES    VAULT_CAPATH           VAULT_CLIENT_CERT
VAULT_CLIENT_KEY      VAULT_NAMESPACE        VAULT_TLS_SERVER_NAME
VAULT_PROXY_ADDR      VAULT_HTTP_PROXY       VAULT_HTTP_ADDR
VAULT_HEADERS         VAULT_WRAP_TTL         VAULT_SKIP_VERIFY
VAULT_SRV_LOOKUP      VAULT_CLIENT_TIMEOUT   VAULT_MAX_RETRIES
VAULT_RATE_LIMIT      VAULT_DISABLE_REDIRECTS
```

Fish represents a cleared value with an exported empty session binding. This
keeps Vault's `os.Getenv` behavior equivalent to unset, updates caller-local
bindings, and shadows—without deleting—any persistent Fish universal value.

`VAULT_TOKEN`, `VAULT_MFA`, and `VAULT_HEADERS` are cleared by default.
`--keep-token` preserves only `VAULT_TOKEN` and should be used only when the
destination metadata has been reviewed. It prints the same bound destination
details to standard error before activation; if no ambient token exists, the
global token-helper warning remains enabled.

You can render without changing the saved default:

```sh
vaultctx env prod-admin --shell sh
vaultctx env prod-admin --shell fish
vaultctx env prod-admin --shell powershell
vaultctx env prod-admin --shell json
```

Standard output contains only the activation program or machine-readable data;
warnings go to standard error, so command substitution remains parseable.

## Safe command execution

By default, `exec` removes ambient token/MFA/header values and sets a fresh
256-bit random non-secret sentinel token. This prevents the Vault CLI from silently
falling back to its global `~/.vault-token` after `VAULT_ADDR` changes; secure
randomness failure aborts the command. A collision with a real opaque Vault
token is theoretically possible but cryptographically negligible:

```sh
# Session-token inputs are blocked; method/provider credentials may still apply.
vaultctx exec prod-admin -- vault status

# Explicitly permit lookup from the configured Vault token helper.
vaultctx exec --allow-token-helper prod-admin -- vault kv list secret/

# Explicitly forward the current VAULT_TOKEN for this one process.
vaultctx exec --forward-ambient-token prod-admin -- vault token lookup

# Fail closed if automation resolves an unexpected current context.
vaultctx exec --expect-context=prod-admin -- vault status

# Bind automation to the address/namespace/TLS-path/agent/proxy metadata.
digest=$(vaultctx fingerprint prod-admin)
vaultctx exec --expect-context=prod-admin --expect-fingerprint="$digest" -- vault status
```

Before every command, `vaultctx` prints the exact context, address, namespace,
fingerprint, configured agent/proxy, and auth mode to standard error. Ambient
proxy or certificate override *names* are also reported when present, but their
possibly credentialed values are hidden. It streams child input/output directly,
preserves numeric and signal-derived child exit codes, and never logs or buffers
Vault output or passthrough arguments. An executable name can appear in a local
resolution/start error.

On macOS/Linux, a non-interactive child receives its own process group;
cancellation forwards SIGINT/SIGTERM, allows a short cleanup window, then
terminates remaining same-group descendants. A terminal-attached child stays in
the foreground group so prompts continue to work. Other platforms cancel only
the direct child, and a process that deliberately daemonizes out of its group is
outside this portable cleanup boundary.

Vault command flags take precedence over environment variables. `exec` does not
rewrite or police an operator's command, so flags such as `-address`,
`-namespace`, or `-tls-skip-verify` can intentionally bypass the selected
context. Review passthrough commands just as carefully as the context itself.

This default blocks only `VAULT_TOKEN`, `VAULT_MFA`, `VAULT_HEADERS`, and
token-helper lookup fallback—not every authentication channel. It does not
isolate helper writes: a successful `vault login` can still store its token
unless that command uses `-no-store` (or an equivalent non-storing mode). A
context may still enable
context-bound mTLS or Vault Agent auto-auth. Explicit login/auth commands may
also consume inherited method or cloud-provider credentials such as
`VAULT_AUTH_GITHUB_TOKEN`, `VAULT_LDAP_PASSWORD`, AWS, Azure, or GCP variables;
`vaultctx` cannot safely enumerate every current and future plugin input. Review
the command and parent environment as well as the agent/destination fingerprint.

For an intentionally non-persistent login, use a command such as
`vaultctx exec CONTEXT -- vault login -no-store ...` and handle the returned
token according to your workstation policy.

The official Vault CLI uses one global `~/.vault-token` by default. Shell
activation can clear `VAULT_TOKEN`, but it cannot stop a later Vault process
from reading that file. Administrator workstations should configure a custom
token helper keyed by at least `(VAULT_ADDR, VAULT_NAMESPACE)`; HashiCorp
documents the [`get`, `store`, and `erase` contract](https://developer.hashicorp.com/vault/docs/commands/token-helper).

## Context commands

```text
vaultctx                         fuzzy/numbered saved-default selection
vaultctx NAME                    save default by exact name (shell unchanged)
vaultctx add NAME [flags]        add connection metadata
vaultctx import NAME [flags]     import allowlisted connection variables
vaultctx list [--json]           list contexts
vaultctx current [--json]        show per-shell or saved current context
vaultctx fingerprint [NAME|-]    hash connection metadata (including TLS paths)
vaultctx use [NAME|-]            save default (or emit activation with --shell)
vaultctx env [NAME|-]            render an activation program
vaultctx exec [NAME|-] -- ...    execute in an isolated environment
vaultctx delete NAME --yes       remove a local context
vaultctx doctor [NAME]           report local resolution, file types, and modes
vaultctx shell-init SHELL        emit the vctx wrapper
```

The default config location comes from Go's user config directory. Override it
with an **absolute** `VAULTCTX_CONFIG` path, which is especially useful for tests.
The file is strict JSON, limited to 1 MiB and 256 contexts, and written with
`0600` POSIX mode inside a `0700` directory. Unknown, non-canonical, or duplicate
keys, symlinked config files, unsafe mode bits, malformed URLs, credential-shaped
unknown fields, and relative TLS paths are rejected. Values in legitimate
metadata fields are not secret-scanned.

The destination fingerprint covers the stored address, namespace, TLS server
name, CA/client-certificate **path strings**, agent address, and proxy address.
It deliberately does not read or hash certificate/key file contents, and it
does not include the description.

For `VAULTCTX_CONFIG` overrides, the immediate parent must already be a real
directory that passes the platform ownership/POSIX-mode checks, or must not
exist so `vaultctx` can create it as `0700`.
The tool never changes permissions on an existing home, project, or shared temp
directory. On macOS, owner and POSIX mode checks do not inspect extended ACLs;
use a private config directory without inherited ACL entries. Windows ACL
ownership is not verified in this MVP.

## Security boundaries

- Contexts are security-sensitive even though the schema has no credential
  fields: a modified address can redirect a future administrator request.
- `vaultctx` is not an authorization layer. Vault ACL policies remain the only
  reliable control over destructive or privileged operations.
- Context selection never contacts Vault. `doctor` is offline in this MVP.
- `fzf` receives only display metadata through NUL-delimited stdin. It runs by
  absolute path with a minimal environment; `VAULT_*` and `FZF_DEFAULT_*` hooks
  are not inherited.
- Do not run `vaultctx` setuid, as root, with `sudo`, or from an untrusted binary
  directory.
- General upper/lowercase proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`,
  `ALL_PROXY`, and `NO_PROXY`), `SSL_CERT_FILE`/`SSL_CERT_DIR`, and `GODEBUG`
  are owned by the parent shell and inherited by `exec`. Their names (but not
  values) are called out in the pre-exec notice and by `doctor`; use the
  context's explicit `--proxy-address` for Vault-specific routing and audit
  TLS-relaxing Go runtime settings before administrator operations.

Read [SECURITY.md](SECURITY.md) before production use.

## Development and review gates

```sh
make fmt-check
make vet
make test
make race
```

Every change to credential binding, config storage, shell rendering, selection,
or subprocess execution requires a dedicated security review. See
[CONTRIBUTING.md](CONTRIBUTING.md) and [docs/architecture.md](docs/architecture.md).

## Roadmap

1. Complete POSIX directory-descriptor hardening, ownership checks, shell
   completions, rename, and signed cross-platform releases.
2. Add opt-in, bounded `doctor --connect` checks that correctly distinguish a
   sealed Vault (`vault status` exit 2) from an unreachable one.
3. Add an OS-keychain token helper keyed by canonical destination metadata.
4. Add read-only team context overlays and signed context bundles.

This repository intentionally has no license yet. Choose and add one before
publishing or accepting external contributions.
