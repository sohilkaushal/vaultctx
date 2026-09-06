# Security policy

`vaultctx` changes where administrator credentials may eventually be sent. Its
configuration is therefore security-sensitive. The schema has no credential
fields, but free-form metadata is not secret-scanned; never put a secret in a
name, namespace, description, or path.

## Supported status

This repository is an MVP and has not yet published a supported release. Do not
use it as the only safeguard around production Vault administrator access.

## Reporting a vulnerability

Do not open a public issue containing tokens, Vault output, internal addresses,
namespaces, certificate paths, exploit details, or other operational metadata.

The project's security contact route is GitHub private vulnerability reporting,
handled by the repository maintainer. Use
[Report a vulnerability](https://github.com/sohilkaushal/vaultctx/security/advisories/new)
to open a private report. Keep exploit details and follow-up discussion inside
that private advisory rather than public issues or pull requests.

**Release preparation status:** enabling this route in repository settings has
not yet been verified. Public release is blocked until it is enabled and the
reporting form is available. If the form is unavailable, do not post sensitive
details publicly; wait for the private route to become available.

Include the affected version or commit, OS and shell versions, expected versus
actual behavior, impact, and minimal reproduction steps with fake data. Redact
credentials and operational metadata from any logs or screenshots. A report
does not need to contain a working exploit to be useful.

Never send a real token as a reproduction. Use a clear canary such as
`VAULTCTX_TEST_CANARY` against a disposable Vault instance.

The maintainer will assess reports and coordinate fixes and disclosure through
the private advisory. This MVP offers no guaranteed response or remediation
time and no bug-bounty commitment.

## Stop-ship areas

Changes in these areas require an independent security reviewer and adversarial
tests:

- destination identity and credential forwarding;
- config parsing, locking, permissions, and atomic replacement;
- shell quoting or generated activation code;
- `fzf` invocation, candidate encoding, or inherited environment;
- executable resolution, child environment, I/O, signals, and exit status;
- HTTP, TLS, CA, client certificate, or proxy behavior.

## Explicit non-goals

- `vaultctx` does not replace Vault policies or operator approval workflows.
- It does not make an administrator shell read-only.
- It does not protect against a compromised user account, kernel, Vault binary,
  fzf binary, shell, or explicitly trusted certificate/key file.
- Shell activation cannot isolate Vault's default global `~/.vault-token`.
- Bash/Zsh `vctx` activation is top-level-only and refuses nested function
  calls; scripts and functions should use `vaultctx exec`.
- The default exec sentinel blocks token-helper lookup fallback, not helper
  writes; successful login commands require `-no-store` when persistence is
  unwanted.
- `exec` is not a general credential sandbox. Explicit login/auth commands can
  consume inherited auth-method or cloud-provider environment credentials.
- The MVP does not make network calls and does not validate a remote server's
  identity or health.

## Production checklist

- Build from a reviewed commit and verify the binary provenance.
- Use HTTPS and trusted CA material; never add `VAULT_SKIP_VERIFY` externally.
- Configure a token helper keyed by address and namespace.
- Keep context and client-key files owner-only. On macOS, independently inspect
  and remove inherited extended ACL entries; this MVP checks UID and POSIX mode
  bits but not extended ACLs.
- Run `vaultctx doctor` and the full race-enabled test suite.
- Confirm the address, namespace, proxy, and auth mode printed before `exec`.
- Review any inherited proxy, trust-store, or `GODEBUG` warning; values are
  hidden because they can themselves contain sensitive metadata.
- Keep Vault policies least-privileged and use a separate break-glass workflow.
