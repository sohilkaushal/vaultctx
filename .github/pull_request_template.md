## Change

Describe the operator-visible behavior and compatibility impact.

## Verification

- [ ] `make check`
- [ ] `make race`
- [ ] Success, failure, cancellation, and malformed-input paths are tested
- [ ] Standard output remains machine-safe; diagnostics go to standard error
- [ ] No token, MFA value, Vault output, or passthrough argument is logged

## Security review

- [ ] This does not touch a stop-ship area in `SECURITY.md`
- [ ] Or: an independent security reviewer approved the change

Review destination binding, shell quoting, config integrity, selector isolation,
and subprocess environments explicitly when relevant.
