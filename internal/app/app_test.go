package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

type fixedPicker struct {
	name string
	err  error
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func (p fixedPicker) Select(context.Context, map[string]config.Context, string) (string, error) {
	return p.name, p.err
}

type testApplication struct {
	app    *App
	out    *bytes.Buffer
	errOut *bytes.Buffer
	env    map[string]string
}

func newTestApplication(t *testing.T) *testApplication {
	t.Helper()
	dir := t.TempDir()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	env := map[string]string{"PATH": os.Getenv("PATH")}
	application := &App{
		In:       strings.NewReader(""),
		Out:      out,
		Err:      errOut,
		Store:    config.NewStore(filepath.Join(dir, "config", "config.json")),
		Picker:   fixedPicker{name: "dev"},
		Getenv:   func(key string) string { return env[key] },
		Environ:  func() []string { return mapEnvironment(env) },
		Command:  exec.CommandContext,
		HomeDir:  func() (string, error) { return dir, nil },
		LookPath: exec.LookPath,
		Version:  "test",
	}
	return &testApplication{app: application, out: out, errOut: errOut, env: env}
}

func mapEnvironment(values map[string]string) []string {
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func (a *testApplication) execute(t *testing.T, args ...string) int {
	t.Helper()
	a.out.Reset()
	a.errOut.Reset()
	return a.app.Execute(context.Background(), args)
}

func (a *testApplication) mustExecute(t *testing.T, args ...string) {
	t.Helper()
	if code := a.execute(t, args...); code != 0 {
		t.Fatalf("Execute(%q) code = %d; stderr = %q", args, code, a.errOut.String())
	}
}

func TestContextLifecycleAndPerShellPrevious(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.vault.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.vault.example:8200", "--namespace", "admin")

	testApp.env["VAULTCTX_CONTEXT"] = "dev"
	cfgBefore, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	testApp.env["VAULTCTX_FINGERPRINT"] = cfgBefore.Contexts["dev"].Fingerprint()
	testApp.env["VAULTCTX_PREVIOUS"] = "old"
	testApp.env["VAULT_TOKEN"] = "canary-token"
	testApp.mustExecute(t, "use", "prod", "--shell=sh")
	script := testApp.out.String()
	for _, expected := range []string{
		"unset VAULT_TOKEN",
		"export VAULT_ADDR='https://prod.vault.example:8200'",
		"export VAULT_NAMESPACE='admin'",
		"export VAULTCTX_CONTEXT='prod'",
		"export VAULTCTX_PREVIOUS='dev'",
		"export VAULTCTX_FINGERPRINT='sha256:",
		"export VAULTCTX_PREVIOUS_FINGERPRINT='sha256:",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("activation script missing %q:\n%s", expected, script)
		}
	}

	cfg, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "dev" || cfg.Previous != "" || cfg.PreviousFingerprint != "" {
		t.Fatalf("shell activation changed saved current/previous/fingerprint to %q/%q/%q", cfg.Current, cfg.Previous, cfg.PreviousFingerprint)
	}

	testApp.env["VAULTCTX_CONTEXT"] = "prod"
	testApp.env["VAULTCTX_FINGERPRINT"] = cfg.Contexts["prod"].Fingerprint()
	testApp.env["VAULTCTX_PREVIOUS"] = "dev"
	testApp.env["VAULTCTX_PREVIOUS_FINGERPRINT"] = cfg.Contexts["dev"].Fingerprint()
	testApp.mustExecute(t, "env", "-", "--shell=json")
	if !strings.Contains(testApp.out.String(), `"VAULTCTX_CONTEXT": "dev"`) {
		t.Fatalf("previous-context JSON activation = %s", testApp.out.String())
	}
}

func TestReplacedPreviousContextFailsClosedForForwardedToken(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "first", "--address", "https://first.example:8200")
	testApp.mustExecute(t, "add", "second", "--address", "https://second.example:8200")
	testApp.mustExecute(t, "use", "second")
	testApp.mustExecute(t, "add", "first", "--address", "https://changed.example:8200", "--force")
	testApp.env["VAULT_TOKEN"] = "FORWARDED_CANARY"
	if code := testApp.execute(t, "use", "-", "--shell=sh", "--keep-token"); code == 0 {
		t.Fatal("keep-token activation accepted a replaced previous context")
	}
	started := false
	testApp.app.Command = func(context.Context, string, ...string) *exec.Cmd {
		started = true
		return nil
	}
	if code := testApp.execute(t, "exec", "--forward-ambient-token", "-", "--", "true"); code == 0 {
		t.Fatal("exec accepted a replaced previous context")
	}
	if started {
		t.Fatal("exec started after previous-context fingerprint mismatch")
	}
	if !strings.Contains(testApp.errOut.String(), "changed since it was selected") {
		t.Fatalf("previous-context mismatch error = %s", testApp.errOut.String())
	}
}

func TestChangedActiveContextFailsClosedUntilReactivation(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://old.example:8200")
	before, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	testApp.env["VAULTCTX_CONTEXT"] = "prod"
	testApp.env["VAULTCTX_FINGERPRINT"] = before.Contexts["prod"].Fingerprint()

	testApp.mustExecute(t, "add", "prod", "--address", "https://new.example:8200", "--force")
	if code := testApp.execute(t, "current"); code == 0 {
		t.Fatal("current accepted stale shell metadata after context replacement")
	}
	if !strings.Contains(testApp.errOut.String(), "changed since activation") {
		t.Fatalf("stale-shell error = %s", testApp.errOut.String())
	}

	testApp.mustExecute(t, "use", "prod", "--shell=sh")
	after, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	newFingerprint := after.Contexts["prod"].Fingerprint()
	if newFingerprint == testApp.env["VAULTCTX_FINGERPRINT"] || !strings.Contains(testApp.out.String(), newFingerprint) {
		t.Fatalf("reactivation did not emit the replacement fingerprint: %s", testApp.out.String())
	}
}

func TestImportOmitsCredentialsAndRejectsTLSBypass(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.env["VAULT_ADDR"] = "https://vault.example:8200"
	testApp.env["VAULT_NAMESPACE"] = "admin"
	testApp.env["VAULT_TOKEN"] = "must-not-persist"
	testApp.env["VAULT_MFA"] = "must-not-persist"
	testApp.env["VAULT_CACERT_BYTES"] = "INLINE_CA_IMPORT_CANARY"
	testApp.env["VAULT_WRAP_TTL"] = "WRAP_IMPORT_CANARY"
	testApp.mustExecute(t, "import", "prod")

	data, err := os.ReadFile(testApp.app.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-persist")) || bytes.Contains(data, []byte("VAULT_TOKEN")) ||
		bytes.Contains(data, []byte("INLINE_CA_IMPORT_CANARY")) || bytes.Contains(data, []byte("WRAP_IMPORT_CANARY")) {
		t.Fatalf("credential leaked into config: %s", data)
	}

	testApp.env["VAULT_SKIP_VERIFY"] = "1"
	if code := testApp.execute(t, "import", "unsafe", "--force"); code == 0 {
		t.Fatal("import accepted VAULT_SKIP_VERIFY=1")
	}
}

func TestHTTPRequiresAcknowledgementAndLoopback(t *testing.T) {
	testApp := newTestApplication(t)
	if code := testApp.execute(t, "add", "dev", "--address", "http://127.0.0.1:8200"); code == 0 {
		t.Fatal("HTTP context succeeded without --allow-http")
	}
	testApp.mustExecute(t, "add", "dev", "--address", "http://127.0.0.1:8200", "--allow-http")
	if code := testApp.execute(t, "add", "remote", "--address", "http://vault.example:8200", "--allow-http"); code == 0 {
		t.Fatal("non-loopback HTTP context was accepted")
	}
}

func TestAddAndImportRejectInvalidUTF8WithoutCreatingConfig(t *testing.T) {
	invalid := strings.Repeat(string([]byte{0xff}), 86)
	for _, operation := range []func(*testApplication) int{
		func(testApp *testApplication) int {
			return testApp.execute(t, "add", "prod", "--address", "https://vault.example", "--namespace", invalid)
		},
		func(testApp *testApplication) int {
			testApp.env["VAULT_ADDR"] = "https://vault.example"
			testApp.env["VAULT_NAMESPACE"] = invalid
			return testApp.execute(t, "import", "prod")
		},
	} {
		testApp := newTestApplication(t)
		if code := operation(testApp); code == 0 {
			t.Fatal("invalid UTF-8 mutation succeeded")
		}
		if !strings.Contains(testApp.errOut.String(), "valid UTF-8") {
			t.Fatalf("invalid UTF-8 error = %q", testApp.errOut.String())
		}
		if _, err := os.Stat(testApp.app.Store.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid UTF-8 mutation created a config: %v", err)
		}
	}
}

func TestAddDoesNotReflectMalformedURLSecrets(t *testing.T) {
	testApp := newTestApplication(t)
	canary := "URL_SUPERSECRET_CANARY"
	if code := testApp.execute(t, "add", "prod", "--address", "https://admin:"+canary+"@vault.example:%zz"); code == 0 {
		t.Fatal("malformed URL succeeded")
	}
	if strings.Contains(testApp.errOut.String(), canary) {
		t.Fatalf("malformed URL canary was reflected to stderr: %s", testApp.errOut.String())
	}
}

func TestExecCredentialModesAndExitCode(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200", "--namespace", "admin")
	testApp.env["VAULT_TOKEN"] = "CANARY_TOKEN"
	testApp.env["VAULT_MFA"] = "CANARY_MFA"
	testApp.env["VAULT_HEADERS"] = `{"Authorization":"Bearer HEADERS_CANARY"}`
	testApp.env["VAULT_CACERT_BYTES"] = "INLINE_CA_EXEC_CANARY"
	testApp.env["VAULT_WRAP_TTL"] = "WRAP_EXEC_CANARY"
	testApp.env["VAULT_SKIP_VERIFY"] = "1"

	testApp.mustExecute(t, "exec", "prod", "--", "env")
	output := testApp.out.String()
	if notice := testApp.errOut.String(); !strings.Contains(notice, "token-helper lookup fallback blocked") || !strings.Contains(notice, "helper writes not isolated") {
		t.Fatalf("default exec notice overclaims token-helper isolation: %s", notice)
	}
	if !strings.Contains(output, "VAULT_TOKEN="+blockedTokenPrefix) {
		t.Fatalf("default exec did not block token helper: %s", output)
	}
	for _, forbidden := range []string{"CANARY_TOKEN", "CANARY_MFA", "HEADERS_CANARY", "VAULT_HEADERS=", "INLINE_CA_EXEC_CANARY", "VAULT_CACERT_BYTES=", "WRAP_EXEC_CANARY", "VAULT_WRAP_TTL=", "VAULT_SKIP_VERIFY=1"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("default exec leaked %q: %s", forbidden, output)
		}
	}

	testApp.mustExecute(t, "exec", "--allow-token-helper", "prod", "--", "env")
	if !strings.Contains(testApp.errOut.String(), "token-helper lookup explicitly allowed") {
		t.Fatalf("allow-token-helper notice is unclear: %s", testApp.errOut.String())
	}
	if strings.Contains(testApp.out.String(), "VAULT_TOKEN=") || strings.Contains(testApp.out.String(), "VAULT_HEADERS=") {
		t.Fatalf("token-helper mode retained ambient token: %s", testApp.out.String())
	}

	testApp.mustExecute(t, "exec", "--forward-ambient-token", "prod", "--", "env")
	output = testApp.out.String()
	if !strings.Contains(output, "VAULT_TOKEN=CANARY_TOKEN") || strings.Contains(output, "CANARY_MFA") || strings.Contains(output, "VAULT_HEADERS=") {
		t.Fatalf("explicit token forwarding environment is wrong: %s", output)
	}

	if code := testApp.execute(t, "exec", "prod", "--", "sh", "-c", "exit 7"); code != 7 {
		t.Fatalf("child exit code = %d, want 7; stderr=%s", code, testApp.errOut.String())
	}
}

func TestExecUsesFreshTokenHelperBlockerAndFailsClosedOnEntropyError(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	testApp.env["VAULT_TOKEN"] = "AMBIENT_CANARY"

	testApp.mustExecute(t, "exec", "prod", "--", "env")
	first := environmentOutputValue(testApp.out.String(), "VAULT_TOKEN")
	testApp.mustExecute(t, "exec", "prod", "--", "env")
	second := environmentOutputValue(testApp.out.String(), "VAULT_TOKEN")
	if !strings.HasPrefix(first, blockedTokenPrefix) || !strings.HasPrefix(second, blockedTokenPrefix) {
		t.Fatalf("blockers do not use the expected non-secret prefix: %q, %q", first, second)
	}
	if first == second || first == testApp.env["VAULT_TOKEN"] || second == testApp.env["VAULT_TOKEN"] {
		t.Fatalf("blockers are not fresh and distinct: %q, %q", first, second)
	}

	testApp.app.Random = strings.NewReader("")
	started := false
	testApp.app.Command = func(context.Context, string, ...string) *exec.Cmd {
		started = true
		return nil
	}
	if code := testApp.execute(t, "exec", "prod", "--", "true"); code == 0 {
		t.Fatal("exec succeeded when secure randomness failed")
	}
	if started {
		t.Fatal("exec started the child after secure randomness failed")
	}
}

func environmentOutputValue(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

func TestExecExpectationFailsClosed(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	if code := testApp.execute(t, "exec", "--expect-context=dev", "prod", "--", "env"); code == 0 {
		t.Fatal("exec ignored mismatched --expect-context")
	}
	if !strings.Contains(testApp.errOut.String(), `expected context "dev", resolved "prod"`) {
		t.Fatalf("unexpected error: %s", testApp.errOut.String())
	}
	if code := testApp.execute(t, "exec", "--expect-fingerprint=sha256:not-the-destination", "prod", "--", "env"); code == 0 {
		t.Fatal("exec ignored mismatched --expect-fingerprint")
	}

	testApp.mustExecute(t, "fingerprint", "prod")
	digest := strings.TrimSpace(testApp.out.String())
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("fingerprint = %q", digest)
	}
}

func TestExecRejectsEmptySafetyGuardsBeforeStartingCommand(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	called := false
	testApp.app.Command = func(context.Context, string, ...string) *exec.Cmd {
		called = true
		return nil
	}
	for _, guard := range []string{"--expect-context=", "--expect-fingerprint="} {
		if code := testApp.execute(t, "exec", guard, "prod", "--", "env"); code == 0 {
			t.Fatalf("empty guard %q succeeded", guard)
		}
		if called {
			t.Fatalf("empty guard %q started the command", guard)
		}
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://vault.example:8200")
	if code := testApp.execute(t, "delete", "dev"); code == 0 {
		t.Fatal("delete succeeded without --yes")
	}
	testApp.mustExecute(t, "delete", "dev", "--yes")
}

func TestUseRejectsUnknownShellWithoutChangingCurrent(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
	if code := testApp.execute(t, "use", "prod", "--shell=not-a-shell"); code == 0 {
		t.Fatal("unsupported shell succeeded")
	}
	cfg, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "dev" {
		t.Fatalf("unsupported shell changed current to %q", cfg.Current)
	}
}

func TestUseRejectsEmptyShellWithoutChangingCurrent(t *testing.T) {
	for _, args := range [][]string{{"use", "prod", "--shell="}, {"use", "prod", "--shell", ""}} {
		testApp := newTestApplication(t)
		testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
		testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
		if code := testApp.execute(t, args...); code == 0 {
			t.Fatalf("%q accepted an empty shell", args)
		}
		cfg, err := testApp.app.Store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Current != "dev" {
			t.Fatalf("%q changed current to %q", args, cfg.Current)
		}
	}
}

func TestExecNoticeNamesAmbientTransportWithoutLeakingValues(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	testApp.env["HTTPS_PROXY"] = "http://operator:PROXY_CANARY@proxy.example:8080"
	testApp.env["SSL_CERT_FILE"] = "/private/CERT_CANARY.pem"
	testApp.env["GODEBUG"] = "x509sha1=GODEBUG_CANARY"
	testApp.mustExecute(t, "exec", "prod", "--", "true")
	notice := testApp.errOut.String()
	for _, key := range []string{"HTTPS_PROXY", "SSL_CERT_FILE", "GODEBUG", "not fingerprinted"} {
		if !strings.Contains(notice, key) {
			t.Errorf("exec notice missing %q: %s", key, notice)
		}
	}
	for _, secret := range []string{"PROXY_CANARY", "CERT_CANARY", "GODEBUG_CANARY", "operator:"} {
		if strings.Contains(notice, secret) {
			t.Errorf("exec notice leaked %q: %s", secret, notice)
		}
	}
}

func TestExplicitUseRecoversFromStaleShellContext(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
	testApp.env["VAULTCTX_CONTEXT"] = "deleted-context"

	testApp.mustExecute(t, "list")
	if !strings.Contains(testApp.out.String(), "dev") {
		t.Fatalf("list failed to recover from stale shell context: %s", testApp.out.String())
	}
	testApp.mustExecute(t, "use", "prod", "--shell=sh")
	if !strings.Contains(testApp.out.String(), "export VAULTCTX_CONTEXT='prod'") {
		t.Fatalf("explicit use did not recover: %s", testApp.out.String())
	}
	if code := testApp.execute(t, "exec", "--", "env"); code == 0 {
		t.Fatal("implicit exec did not fail closed on stale ambient context")
	}
}

func TestExecFailsClosedWhenSafetyNoticeCannotBeWritten(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	writeErr := errors.New("stderr closed")
	testApp.app.Err = failingWriter{err: writeErr}
	called := false
	testApp.app.Command = func(context.Context, string, ...string) *exec.Cmd {
		called = true
		return nil
	}
	if code := testApp.app.Execute(context.Background(), []string{"exec", "prod", "--", "env"}); code == 0 {
		t.Fatal("exec succeeded without writing its safety notice")
	}
	if called {
		t.Fatal("exec started the child after safety notice failure")
	}
}

func TestActivationWarnsAboutGlobalHelperAfterClearingAmbientToken(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	home := filepath.Dir(filepath.Dir(testApp.app.Store.Path))
	if err := os.WriteFile(filepath.Join(home, ".vault-token"), []byte("TEST_CANARY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testApp.env["VAULT_TOKEN"] = "AMBIENT_CANARY"
	testApp.mustExecute(t, "use", "prod", "--shell=sh")
	if !strings.Contains(testApp.errOut.String(), "global ~/.vault-token") {
		t.Fatalf("activation did not warn about post-clear helper fallback: %s", testApp.errOut.String())
	}
}

func TestKeepTokenActivationPrintsBoundNotice(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200", "--namespace", "admin")
	testApp.env["VAULT_TOKEN"] = "AMBIENT_CANARY"
	testApp.mustExecute(t, "use", "prod", "--shell=sh", "--keep-token")
	notice := testApp.errOut.String()
	for _, expected := range []string{"activation \"prod\"", "address=https://vault.example:8200", "namespace=admin", "fingerprint=sha256:", "ambient VAULT_TOKEN explicitly retained"} {
		if !strings.Contains(notice, expected) {
			t.Errorf("keep-token notice missing %q: %s", expected, notice)
		}
	}
	if strings.Contains(testApp.out.String(), "unset VAULT_TOKEN") {
		t.Fatalf("keep-token activation cleared the token: %s", testApp.out.String())
	}
}

func TestKeepTokenWithoutAmbientTokenStillWarnsAboutGlobalHelper(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	home := filepath.Dir(filepath.Dir(testApp.app.Store.Path))
	if err := os.WriteFile(filepath.Join(home, ".vault-token"), []byte("TEST_CANARY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(testApp.env, "VAULT_TOKEN")
	testApp.mustExecute(t, "env", "prod", "--shell=sh", "--keep-token")
	for _, expected := range []string{"no ambient VAULT_TOKEN exists to retain", "global ~/.vault-token"} {
		if !strings.Contains(testApp.errOut.String(), expected) {
			t.Errorf("empty keep-token warning missing %q: %s", expected, testApp.errOut.String())
		}
	}
}

func TestKeepTokenNoticeFailurePreventsActivationMutation(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
	testApp.env["VAULT_TOKEN"] = "AMBIENT_CANARY"
	testApp.app.Err = failingWriter{err: errors.New("stderr closed")}
	if code := testApp.app.Execute(context.Background(), []string{"use", "prod", "--shell=sh", "--keep-token"}); code == 0 {
		t.Fatal("keep-token activation succeeded without writing its safety notice")
	}
	cfg, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "dev" {
		t.Fatalf("failed keep-token notice changed current to %q", cfg.Current)
	}
}

func TestReadOnlyCommandsHelpAndVersion(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")

	testApp.mustExecute(t, "list", "--json")
	for _, expected := range []string{`"name": "dev"`, `"current": true`, `"fingerprint": "sha256:`} {
		if !strings.Contains(testApp.out.String(), expected) {
			t.Errorf("list JSON missing %q: %s", expected, testApp.out.String())
		}
	}
	testApp.mustExecute(t, "current", "--json")
	if !strings.Contains(testApp.out.String(), `"name":"dev"`) {
		t.Fatalf("current JSON = %s", testApp.out.String())
	}

	for _, args := range [][]string{
		{"help"}, {"add", "--help"}, {"import", "--help"}, {"list", "--help"},
		{"current", "--help"}, {"fingerprint", "--help"}, {"use", "--help"},
		{"env", "--help"}, {"exec", "--help"}, {"delete", "--help"},
		{"doctor", "--help"}, {"shell-init", "--help"},
	} {
		testApp.mustExecute(t, args...)
		if !strings.Contains(strings.ToLower(testApp.out.String()), "usage") && args[0] != "help" {
			t.Errorf("%v help output = %q", args, testApp.out.String())
		}
	}

	testApp.mustExecute(t, "version")
	if testApp.out.String() != "vaultctx test\n" {
		t.Fatalf("version output = %q", testApp.out.String())
	}
	testApp.mustExecute(t, "shell-init", "zsh")
	if !strings.Contains(testApp.out.String(), "vctx()") {
		t.Fatalf("zsh shell init = %s", testApp.out.String())
	}
	testApp.mustExecute(t, "shell-init", "fish")
	if !strings.Contains(testApp.out.String(), "string collect") {
		t.Fatalf("fish shell init = %s", testApp.out.String())
	}
	if code := testApp.execute(t, "shell-init", "unknown"); code == 0 {
		t.Fatal("unsupported shell-init succeeded")
	}
}

func TestInteractiveRootUsesPicker(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
	testApp.app.Picker = fixedPicker{name: "prod"}
	testApp.mustExecute(t)
	if !strings.Contains(testApp.out.String(), `Saved default context "prod"; this shell is unchanged`) {
		t.Fatalf("interactive root output = %q", testApp.out.String())
	}
}

func TestSavedDefaultMakesShellUnchangedAndHidesAmbientValues(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
	testApp.env["VAULT_ADDR"] = "https://ADDRESS_VALUE_CANARY.example"
	testApp.env["VAULT_TOKEN"] = "TOKEN_VALUE_CANARY"
	testApp.mustExecute(t, "use", "prod")
	if !strings.Contains(testApp.out.String(), `Saved default context "prod"; this shell is unchanged. Use vctx or vaultctx exec.`) {
		t.Fatalf("saved-default output is ambiguous: %s", testApp.out.String())
	}
	warning := testApp.errOut.String()
	for _, name := range []string{"VAULT_ADDR", "VAULT_TOKEN", "values hidden", "will not change this shell"} {
		if !strings.Contains(warning, name) {
			t.Errorf("saved-default warning missing %q: %s", name, warning)
		}
	}
	for _, value := range []string{"ADDRESS_VALUE_CANARY", "TOKEN_VALUE_CANARY"} {
		if strings.Contains(warning, value) {
			t.Errorf("saved-default warning leaked %q: %s", value, warning)
		}
	}
}

func TestDoctorReportsLocalSafetyState(t *testing.T) {
	testApp := newTestApplication(t)
	home := filepath.Dir(filepath.Dir(testApp.app.Store.Path))
	caPath := filepath.Join(home, "ca.pem")
	clientCert := filepath.Join(home, "client.pem")
	clientKey := filepath.Join(home, "client-key.pem")
	for _, path := range []string{caPath, clientCert, clientKey} {
		if err := os.WriteFile(path, []byte("test material\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200",
		"--ca-cert", caPath, "--client-cert", clientCert, "--client-key", clientKey)
	testApp.env["http_proxy"] = "http://proxy.example:8080"
	testApp.env["GODEBUG"] = "x509sha1=1"
	testApp.app.LookPath = func(name string) (string, error) { return "/trusted/bin/" + name, nil }
	testApp.mustExecute(t, "doctor", "prod")
	for _, expected := range []string{"[OK]", "client key is a readable regular file", "http_proxy is inherited", "GODEBUG is inherited", "0 error(s)"} {
		if !strings.Contains(testApp.out.String(), expected) {
			t.Errorf("doctor output missing %q:\n%s", expected, testApp.out.String())
		}
	}

	testApp.app.LookPath = func(name string) (string, error) {
		if name == "vault" {
			return "", exec.ErrNotFound
		}
		return "/trusted/bin/fzf", nil
	}
	if code := testApp.execute(t, "doctor"); code != 1 {
		t.Fatalf("doctor without Vault code = %d, want 1", code)
	}
	if !strings.Contains(testApp.out.String(), "Vault CLI was not found as an absolute PATH result") {
		t.Fatalf("doctor failure output = %s", testApp.out.String())
	}
}

func TestDoctorMissingConfigIsWarningNotOwnerOnlyClaim(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.app.LookPath = func(name string) (string, error) { return "/trusted/bin/" + name, nil }
	testApp.mustExecute(t, "doctor")
	if !strings.Contains(testApp.out.String(), "configuration has not been created yet") ||
		strings.Contains(testApp.out.String(), "configuration is strict, valid, and passes owner/POSIX-mode checks") {
		t.Fatalf("missing-config doctor output = %s", testApp.out.String())
	}
}

func TestConfigValidationStatusDoesNotOverclaimUnsupportedPlatforms(t *testing.T) {
	tests := []struct {
		goos   string
		wantOK bool
		want   string
		reject string
	}{
		{goos: "linux", wantOK: true, want: "passes owner/POSIX-mode checks"},
		{goos: "darwin", want: "extended ACLs are not yet verified"},
		{goos: "windows", want: "ACL ownership is not yet verified"},
		{goos: "freebsd", want: "ownership and hard-link checks are not yet verified", reject: "passes owner/POSIX-mode checks"},
		{goos: "openbsd", want: "ownership and hard-link checks are not yet verified", reject: "passes owner/POSIX-mode checks"},
		{goos: "plan9", want: "ownership and hard-link checks are not yet verified", reject: "passes owner/POSIX-mode checks"},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			ok, message := configValidationStatus(tc.goos, "/config.json")
			if ok != tc.wantOK {
				t.Fatalf("configValidationStatus(%q) ok = %v, want %v", tc.goos, ok, tc.wantOK)
			}
			if !strings.Contains(message, tc.want) {
				t.Fatalf("configValidationStatus(%q) = %q, want %q", tc.goos, message, tc.want)
			}
			if tc.reject != "" && strings.Contains(message, tc.reject) {
				t.Fatalf("configValidationStatus(%q) overclaims %q: %s", tc.goos, tc.reject, message)
			}
		})
	}
}

func TestDoctorReportsOutputFailure(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.app.Out = failingWriter{err: errors.New("stdout closed")}
	if code := testApp.app.Execute(context.Background(), []string{"doctor"}); code == 0 {
		t.Fatal("doctor succeeded after its report writer failed")
	}
	if !strings.Contains(testApp.errOut.String(), "write doctor report") {
		t.Fatalf("doctor output failure = %s", testApp.errOut.String())
	}
}

func TestCanceledMutationDoesNotCommit(t *testing.T) {
	testApp := newTestApplication(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := testApp.app.Execute(ctx, []string{"add", "prod", "--address", "https://vault.example:8200"})
	if code != 130 {
		t.Fatalf("canceled add code = %d, want 130", code)
	}
	if _, err := os.Stat(testApp.app.Store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled add created config: %v", err)
	}
}

func TestCanceledActivationDoesNotEmitScript(t *testing.T) {
	for _, args := range [][]string{
		{"use", "prod", "--shell=sh"},
		{"env", "prod", "--shell=sh"},
	} {
		t.Run(args[0], func(t *testing.T) {
			testApp := newTestApplication(t)
			testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
			testApp.out.Reset()
			testApp.errOut.Reset()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if code := testApp.app.Execute(ctx, args); code != 130 {
				t.Fatalf("canceled activation code = %d, want 130; stderr=%q", code, testApp.errOut.String())
			}
			if testApp.out.Len() != 0 {
				t.Fatalf("canceled activation emitted a script: %q", testApp.out.String())
			}
		})
	}
}

func TestFailedShellActivationOutputDoesNotChangeSavedDefault(t *testing.T) {
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "dev", "--address", "https://dev.example:8200")
	testApp.mustExecute(t, "add", "prod", "--address", "https://prod.example:8200")
	testApp.app.Out = failingWriter{err: errors.New("stdout closed")}

	if code := testApp.app.Execute(context.Background(), []string{"use", "prod", "--shell=sh"}); code == 0 {
		t.Fatal("shell activation succeeded after its output failed")
	}
	cfg, err := testApp.app.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "dev" {
		t.Fatalf("failed shell activation changed saved default to %q", cfg.Current)
	}
}
