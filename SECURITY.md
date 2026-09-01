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
Before publishing this project, configure a private security contact and GitHub
private vulnerability reporting. Until then, report findings to the repository
owner through an agreed private channel.

Never send a real token as a reproduction. Use a clear canary such as
`VAULTCTX_TEST_CANARY` against a disposable Vault instance.

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
