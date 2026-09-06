package contextenv

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

func TestManagedVariableListsAreUniqueAndComplete(t *testing.T) {
	t.Parallel()

	assertUnique := func(name string, values []string) {
		t.Helper()
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if _, duplicate := seen[value]; duplicate {
				t.Errorf("%s contains duplicate %q", name, value)
			}
			seen[value] = struct{}{}
		}
	}
	assertUnique("ManagedVariables", ManagedVariables)
	assertUnique("EphemeralCredentialVariables", EphemeralCredentialVariables)

	for _, required := range []string{
		"VAULT_ADDR",
		"VAULT_AGENT_ADDR",
		"VAULT_CACERT",
		"VAULT_CACERT_BYTES",
		"VAULT_CAPATH",
		"VAULT_CLIENT_CERT",
		"VAULT_CLIENT_KEY",
		"VAULT_NAMESPACE",
		"VAULT_TLS_SERVER_NAME",
		"VAULT_PROXY_ADDR",
		"VAULT_HTTP_PROXY",
		"VAULT_HTTP_ADDR",
		"VAULT_HEADERS",
		"VAULT_WRAP_TTL",
		"VAULT_SKIP_VERIFY",
		"VAULT_SRV_LOOKUP",
		"VAULT_CLIENT_TIMEOUT",
		"VAULT_MAX_RETRIES",
		"VAULT_RATE_LIMIT",
		"VAULT_DISABLE_REDIRECTS",
	} {
		if !slices.Contains(ManagedVariables, required) {
			t.Errorf("ManagedVariables does not contain %q", required)
		}
	}
	for _, credential := range []string{"VAULT_TOKEN", "VAULT_MFA"} {
		if !slices.Contains(EphemeralCredentialVariables, credential) {
			t.Errorf("EphemeralCredentialVariables does not contain %q", credential)
		}
		if slices.Contains(ManagedVariables, credential) {
			t.Errorf("%q appears in both managed-variable lists", credential)
		}
	}
}

func TestVariablesReturnsOnlyRepresentedNonEmptyValues(t *testing.T) {
	t.Parallel()

	context := config.Context{
		Address:       "https://vault.example",
		Namespace:     "admin",
		CACert:        "/certs/ca.pem",
		ClientCert:    "/certs/client.pem",
		ClientKey:     "/certs/client-key.pem",
		TLSServerName: "vault.service.internal",
		AgentAddress:  "http://127.0.0.1:8200",
		ProxyAddress:  "https://proxy.example",
		Description:   "must not become an environment variable",
	}
	want := map[string]string{
		"VAULT_ADDR":            "https://vault.example",
		"VAULT_NAMESPACE":       "admin",
		"VAULT_CACERT":          "/certs/ca.pem",
		"VAULT_CLIENT_CERT":     "/certs/client.pem",
		"VAULT_CLIENT_KEY":      "/certs/client-key.pem",
		"VAULT_TLS_SERVER_NAME": "vault.service.internal",
		"VAULT_AGENT_ADDR":      "http://127.0.0.1:8200",
		"VAULT_PROXY_ADDR":      "https://proxy.example",
	}
	if got := Variables(context); !reflect.DeepEqual(got, want) {
		t.Fatalf("Variables() = %#v, want %#v", got, want)
	}

	if got := Variables(config.Context{}); len(got) != 0 {
		t.Fatalf("Variables(empty) = %#v, want empty map", got)
	}
}

func TestApplyDeduplicatesAndClearsStaleSettings(t *testing.T) {
	t.Parallel()

	base := []string{
		"PATH=/usr/bin",
		"DUPLICATE=first",
		"DUPLICATE=last",
		"EMPTY=",
		"WITH_EQUALS=left=right",
		"BROKEN",
		"=missing-name",
	}
	for _, key := range ManagedVariables {
		base = append(base, key+"=stale", key+"=also-stale")
	}
	base = append(base,
		"VAULT_TOKEN=old-token",
		"VAULT_TOKEN=new-token",
		"VAULT_MFA=old-mfa",
	)
	context := config.Context{
		Address:   "https://new-vault.example",
		Namespace: "new-namespace",
	}

	gotSlice := Apply(base, context, false)
	got := environmentMap(t, gotSlice)
	want := map[string]string{
		"DUPLICATE":       "last",
		"EMPTY":           "",
		"PATH":            "/usr/bin",
		"VAULT_ADDR":      "https://new-vault.example",
		"VAULT_NAMESPACE": "new-namespace",
		"WITH_EQUALS":     "left=right",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Apply() = %#v, want %#v", got, want)
	}
	if !sort.StringsAreSorted(gotSlice) {
		t.Fatalf("Apply() result is not sorted: %v", gotSlice)
	}
	for _, key := range ManagedVariables {
		if key == "VAULT_ADDR" || key == "VAULT_NAMESPACE" {
			continue
		}
		if _, exists := got[key]; exists {
			t.Errorf("stale managed variable %q survived Apply", key)
		}
	}
}

func TestApplyKeepTokenNeverKeepsMFA(t *testing.T) {
	t.Parallel()

	base := []string{
		"SAFE=value",
		"VAULT_TOKEN=first",
		"VAULT_TOKEN=last",
		"VAULT_MFA=one-time-secret",
	}
	context := config.Context{Address: "https://vault.example"}

	withoutToken := environmentMap(t, Apply(base, context, false))
	if _, exists := withoutToken["VAULT_TOKEN"]; exists {
		t.Error("Apply(..., keepToken=false) retained VAULT_TOKEN")
	}
	if _, exists := withoutToken["VAULT_MFA"]; exists {
		t.Error("Apply(..., keepToken=false) retained VAULT_MFA")
	}

	withToken := environmentMap(t, Apply(base, context, true))
	if got := withToken["VAULT_TOKEN"]; got != "last" {
		t.Errorf("Apply(..., keepToken=true) VAULT_TOKEN = %q, want %q", got, "last")
	}
	if _, exists := withToken["VAULT_MFA"]; exists {
		t.Error("Apply(..., keepToken=true) retained VAULT_MFA")
	}
}

func TestApplyCaseInsensitiveWindowsEnvironmentDeduplicatesAndClearsMixedCase(t *testing.T) {
	t.Parallel()

	base := []string{
		"Path=first",
		"PATH=last",
		"vault_addr=https://stale.example",
		"vault_skip_verify=true",
		"VaUlT_ToKeN=first-token",
		"vault_token=last-token",
		"vault_mfa=one-time-secret",
	}
	context := config.Context{Address: "https://new.example"}

	withoutToken := apply(base, context, false, true)
	wantWithoutToken := []string{
		"PATH=last",
		"VAULT_ADDR=https://new.example",
	}
	if !reflect.DeepEqual(withoutToken, wantWithoutToken) {
		t.Fatalf("case-insensitive apply(..., keepToken=false) = %v, want %v", withoutToken, wantWithoutToken)
	}

	withToken := apply(base, context, true, true)
	wantWithToken := []string{
		"PATH=last",
		"VAULT_ADDR=https://new.example",
		"vault_token=last-token",
	}
	if !reflect.DeepEqual(withToken, wantWithToken) {
		t.Fatalf("case-insensitive apply(..., keepToken=true) = %v, want %v", withToken, wantWithToken)
	}
	for _, entry := range withToken {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "VAULT_SKIP_VERIFY") || strings.EqualFold(key, "VAULT_MFA") {
			t.Errorf("stale mixed-case secret/connection variable survived: %q", entry)
		}
	}
}

func TestShellQuoters(t *testing.T) {
	t.Parallel()

	posix := []struct {
		value string
		want  string
	}{
		{"", "''"},
		{"plain", "'plain'"},
		{"O'Brien", `'O'"'"'Brien'`},
		{"$HOME; $(touch /tmp/pwned)", "'$HOME; $(touch /tmp/pwned)'"},
	}
	for _, tc := range posix {
		if got := quotePOSIX(tc.value); got != tc.want {
			t.Errorf("quotePOSIX(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}

	fish := []struct {
		value string
		want  string
	}{
		{"", "''"},
		{"plain", "'plain'"},
		{`C:\team's`, `'C:\\team\'s'`},
		{"$HOME; (touch /tmp/pwned)", "'$HOME; (touch /tmp/pwned)'"},
	}
	for _, tc := range fish {
		if got := quoteFish(tc.value); got != tc.want {
			t.Errorf("quoteFish(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}

	powerShell := []struct {
		value string
		want  string
	}{
		{"", "''"},
		{"plain", "'plain'"},
		{"O'Brien", "'O''Brien'"},
		{"$Env:HOME; Remove-Item C:\\important", "'$Env:HOME; Remove-Item C:\\important'"},
	}
	for _, tc := range powerShell {
		if got := quotePowerShell(tc.value); got != tc.want {
			t.Errorf("quotePowerShell(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestPOSIXScriptQuotesHostileValuesAsData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell is not universally available on Windows")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}

	hostile := `team'; printf INJECTED; # $HOME $(id)`
	script, err := Script(config.Context{
		Address:   "https://vault.example",
		Namespace: hostile,
	}, "sh", false, "prod", "previous", "")
	if err != nil {
		t.Fatalf("Script() error = %v", err)
	}
	command := exec.Command(sh, "-c", script+`printf '%s' "$VAULT_NAMESPACE"`)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute generated sh script: %v; output = %q", err, output)
	}
	if got := string(output); got != hostile {
		t.Fatalf("generated script output = %q, want exact hostile value %q", got, hostile)
	}
}

func TestScriptClearsCredentialsAndSetsQuotedMetadata(t *testing.T) {
	t.Parallel()

	context := config.Context{
		Address:   "https://vault.example",
		Namespace: "O'Brien $HOME",
	}
	previousFingerprint := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name             string
		shell            string
		clear            func(string) string
		metadataCurrent  string
		metadataPrevious string
		namespace        string
	}{
		{
			name:             "POSIX",
			shell:            "sh",
			clear:            func(key string) string { return "unset " + key + " &&" },
			metadataCurrent:  `export VAULTCTX_CONTEXT='prod'"'"'primary'`,
			metadataPrevious: `export VAULTCTX_PREVIOUS='stage $HOME'`,
			namespace:        `export VAULT_NAMESPACE='O'"'"'Brien $HOME'`,
		},
		{
			name:             "fish",
			shell:            "fish",
			clear:            func(key string) string { return "set -gx " + key + " ''\n" },
			metadataCurrent:  `set -gx VAULTCTX_CONTEXT 'prod\'primary'`,
			metadataPrevious: `set -gx VAULTCTX_PREVIOUS 'stage $HOME'`,
			namespace:        `set -gx VAULT_NAMESPACE 'O\'Brien $HOME'`,
		},
		{
			name:             "PowerShell",
			shell:            "powershell",
			clear:            func(key string) string { return "Remove-Item Env:" + key + " -ErrorAction Stop }" },
			metadataCurrent:  `Set-Item Env:VAULTCTX_CONTEXT -Value 'prod''primary' -ErrorAction Stop`,
			metadataPrevious: `Set-Item Env:VAULTCTX_PREVIOUS -Value 'stage $HOME' -ErrorAction Stop`,
			namespace:        `Set-Item Env:VAULT_NAMESPACE -Value 'O''Brien $HOME' -ErrorAction Stop`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, err := Script(context, tc.shell, true, "prod'primary", "stage $HOME", previousFingerprint)
			if err != nil {
				t.Fatalf("Script() error = %v", err)
			}
			for _, key := range ManagedVariables {
				wantCount := 1
				if key == "VAULT_ADDR" || key == "VAULT_NAMESPACE" {
					wantCount = 0
				}
				if count := strings.Count(script, tc.clear(key)); count != wantCount {
					t.Errorf("clear command for %s occurs %d times, want %d\n%s", key, count, wantCount, script)
				}
			}
			if strings.Contains(script, tc.clear("VAULT_TOKEN")) {
				t.Error("keepToken=true script clears VAULT_TOKEN")
			}
			if !strings.Contains(script, tc.clear("VAULT_MFA")) {
				t.Error("keepToken=true script does not clear VAULT_MFA")
			}
			for _, key := range []string{"VAULT_ADDR", "VAULT_NAMESPACE"} {
				if strings.Contains(script, tc.clear(key)) {
					t.Errorf("script contradicts its set plan by also clearing %s", key)
				}
			}
			for _, key := range []string{"VAULTCTX_CONTEXT", "VAULTCTX_PREVIOUS", "VAULTCTX_FINGERPRINT", "VAULTCTX_PREVIOUS_FINGERPRINT"} {
				if !strings.Contains(script, tc.clear(key)) {
					t.Errorf("script does not clear old metadata %s before applying the new plan", key)
				}
			}
			if !strings.Contains(script, previousFingerprint) {
				t.Errorf("script does not set previous fingerprint:\n%s", script)
			}
			for label, line := range map[string]string{
				"current metadata":  tc.metadataCurrent,
				"previous metadata": tc.metadataPrevious,
				"namespace":         tc.namespace,
			} {
				if !strings.Contains(script, line) {
					t.Errorf("script does not contain safely quoted %s line %q\n%s", label, line, script)
				}
			}
		})
	}
}

func TestScriptTokenClearingPolicyForEveryShell(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shell      string
		clearToken string
		clearMFA   string
	}{
		{shell: "sh", clearToken: "unset VAULT_TOKEN &&", clearMFA: "unset VAULT_MFA &&"},
		{shell: "fish", clearToken: "set -gx VAULT_TOKEN ''\n", clearMFA: "set -gx VAULT_MFA ''\n"},
		{shell: "powershell", clearToken: "Remove-Item Env:VAULT_TOKEN -ErrorAction Stop }", clearMFA: "Remove-Item Env:VAULT_MFA -ErrorAction Stop }"},
	}
	context := config.Context{Address: "https://vault.example"}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.shell, func(t *testing.T) {
			t.Parallel()
			clearing, err := Script(context, tc.shell, false, "prod", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(clearing, tc.clearToken) || !strings.Contains(clearing, tc.clearMFA) {
				t.Fatalf("keepToken=false script does not clear both token and MFA:\n%s", clearing)
			}

			keeping, err := Script(context, tc.shell, true, "prod", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(keeping, tc.clearToken) {
				t.Errorf("keepToken=true script clears VAULT_TOKEN:\n%s", keeping)
			}
			if !strings.Contains(keeping, tc.clearMFA) {
				t.Errorf("keepToken=true script does not clear VAULT_MFA:\n%s", keeping)
			}
		})
	}
}

func TestJSONScriptContainsExactSetAndUnsetPlan(t *testing.T) {
	t.Parallel()

	context := config.Context{
		Address:      "https://vault.example",
		Namespace:    "admin",
		ProxyAddress: "https://proxy.example",
	}
	previousFingerprint := "sha256:" + strings.Repeat("b", 64)
	script, err := Script(context, "json", true, "prod", "staging", previousFingerprint)
	if err != nil {
		t.Fatalf("Script() error = %v", err)
	}
	var payload struct {
		Set   map[string]string `json:"set"`
		Unset []string          `json:"unset"`
	}
	if err := json.Unmarshal([]byte(script), &payload); err != nil {
		t.Fatalf("json.Unmarshal(Script()) error = %v; script = %q", err, script)
	}
	wantSet := map[string]string{
		"VAULT_ADDR":                    "https://vault.example",
		"VAULT_NAMESPACE":               "admin",
		"VAULT_PROXY_ADDR":              "https://proxy.example",
		"VAULTCTX_CONTEXT":              "prod",
		"VAULTCTX_PREVIOUS":             "staging",
		"VAULTCTX_FINGERPRINT":          context.Fingerprint(),
		"VAULTCTX_PREVIOUS_FINGERPRINT": previousFingerprint,
	}
	if !reflect.DeepEqual(payload.Set, wantSet) {
		t.Errorf("JSON set plan = %#v, want %#v", payload.Set, wantSet)
	}
	wantUnset := make([]string, 0, len(ManagedVariables)+1)
	for _, key := range ManagedVariables {
		if _, set := wantSet[key]; !set {
			wantUnset = append(wantUnset, key)
		}
	}
	wantUnset = append(wantUnset, "VAULT_MFA")
	if !reflect.DeepEqual(payload.Unset, wantUnset) {
		t.Errorf("JSON unset plan = %#v, want %#v", payload.Unset, wantUnset)
	}
	for _, key := range payload.Unset {
		if _, alsoSet := payload.Set[key]; alsoSet {
			t.Errorf("JSON plan contains %q in both set and unset", key)
		}
	}
	if !strings.HasSuffix(script, "\n") {
		t.Error("JSON script does not end with a newline")
	}
}

func TestScriptAlwaysClearsEmptyContextMetadata(t *testing.T) {
	t.Parallel()

	script, err := Script(config.Context{Address: "https://vault.example"}, "sh", false, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"unset VAULTCTX_CONTEXT &&", "unset VAULTCTX_PREVIOUS &&", "unset VAULTCTX_FINGERPRINT &&", "unset VAULTCTX_PREVIOUS_FINGERPRINT &&"} {
		if !strings.Contains(script, line) {
			t.Errorf("script missing %q", line)
		}
	}
	if strings.Contains(script, "export VAULTCTX_CONTEXT=") || strings.Contains(script, "export VAULTCTX_PREVIOUS=") {
		t.Errorf("script sets empty context metadata:\n%s", script)
	}
}

func TestScriptIsDeterministic(t *testing.T) {
	t.Parallel()

	context := config.Context{
		Address:       "https://vault.example",
		Namespace:     "admin",
		CACert:        "/tmp/ca.pem",
		TLSServerName: "vault.internal",
		AgentAddress:  "http://127.0.0.1:8200",
		ProxyAddress:  "https://proxy.example",
	}
	previousFingerprint := "sha256:" + strings.Repeat("c", 64)
	want, err := Script(context, "sh", false, "prod", "staging", previousFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 25; index++ {
		got, err := Script(context, "sh", false, "prod", "staging", previousFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("Script() output changed between calls\nfirst:\n%s\nlater:\n%s", want, got)
		}
	}
}

func TestNormalizeShell(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":                                     "sh",
		"bash":                                 "sh",
		" ZSH ":                                "sh",
		"/usr/local/bin/dash":                  "sh",
		`C:\Program Files\PowerShell\pwsh.exe`: "powershell",
		"powershell.exe":                       "powershell",
		"fish":                                 "fish",
		"JSON":                                 "json",
		"tcsh":                                 "tcsh",
	}
	for input, want := range tests {
		if got := NormalizeShell(input); got != want {
			t.Errorf("NormalizeShell(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestScriptRejectsUnsupportedShell(t *testing.T) {
	t.Parallel()

	_, err := Script(config.Context{Address: "https://vault.example"}, "tcsh", false, "", "", "")
	if err == nil || !strings.Contains(err.Error(), `unsupported shell "tcsh"`) {
		t.Fatalf("Script() error = %v, want unsupported-shell error", err)
	}
}

func TestShellInit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shell string
		want  []string
	}{
		{shell: "bash", want: []string{`${BASH_VERSION:-}`, `${#FUNCNAME[@]}`, `refusing nested shell activation`, `command vaultctx use --shell=sh "$@"`, `|| return`, `eval "$_vaultctx_env"`}},
		{shell: "zsh", want: []string{`${ZSH_VERSION:-}`, `${#funcstack[@]}`, `refusing nested shell activation`, `command vaultctx use --shell=sh "$@"`, `|| return`, `eval "$_vaultctx_env"`}},
		{shell: "fish", want: []string{"function vctx --no-scope-shadowing", "command vaultctx use --shell=fish $argv", "$pipestatus[1]", `return "$_vaultctx_command_status"`, "eval $_vaultctx_env"}},
		{shell: "pwsh", want: []string{"Get-Command vaultctx -CommandType Application -ErrorAction Stop", "$vaultctxCommand.Source use --shell=powershell @args", "$vaultctxExitCode = $LASTEXITCODE", "Write-Error", "Invoke-Expression $script -ErrorAction Stop"}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.shell, func(t *testing.T) {
			t.Parallel()
			got, err := ShellInit(tc.shell)
			if err != nil {
				t.Fatalf("ShellInit() error = %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("ShellInit(%q) does not contain %q:\n%s", tc.shell, want, got)
				}
			}
		})
	}
	if _, err := ShellInit("tcsh"); err == nil {
		t.Fatal("ShellInit(tcsh) unexpectedly succeeded")
	}
	if _, err := ShellInit("sh"); err == nil {
		t.Fatal("ShellInit(sh) unexpectedly offered an unsafe generic nested wrapper")
	}
}

func TestBashAndZshShellInitRejectCrossDialectUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells are unavailable on Windows")
	}
	tests := []struct {
		generatedFor string
		runWith      string
	}{
		{generatedFor: "bash", runWith: "zsh"},
		{generatedFor: "zsh", runWith: "bash"},
	}
	for _, tc := range tests {
		t.Run(tc.generatedFor+"-under-"+tc.runWith, func(t *testing.T) {
			shell, err := exec.LookPath(tc.runWith)
			if err != nil {
				t.Skipf("%s is unavailable", tc.runWith)
			}
			init, err := ShellInit(tc.generatedFor)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(shell, "-c", init+"\nvctx prod")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("%s initializer ran under %s:\n%s", tc.generatedFor, tc.runWith, output)
			}
			if !strings.Contains(string(output), "initializer was generated for") {
				t.Fatalf("cross-dialect failure was not explained: %s", output)
			}
		})
	}
}

func TestShellInitRunsAtTopLevelInBashAndZsh(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "vaultctx")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf \"export VAULT_ADDR='https://selected.example'\\n\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Logf("%s not installed; skipping", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			init, err := ShellInit(name)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(path, "-c", init+"\nvctx prod\n[ \"$VAULT_ADDR\" = https://selected.example ]")
			command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("POSIX wrapper failed: %v\n%s", err, output)
			}
		})
	}
}

func TestBashAndZshShellInitRejectNestedScopedActivation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell scoping is unavailable on Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "vaultctx")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf \"unset VAULT_TOKEN && export VAULT_ADDR='https://new.example'\\n\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Logf("%s not installed; skipping", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			init, err := ShellInit(name)
			if err != nil {
				t.Fatal(err)
			}
			commandText := init + `
export VAULT_TOKEN='GLOBAL_TOKEN'
export VAULT_ADDR='https://global-old.example'
deploy() {
  local -x VAULT_TOKEN='CALLER_TOKEN'
  local -x VAULT_ADDR='https://caller-old.example'
  vctx prod
  test "$?" -ne 0 || return 81
  test "$VAULT_TOKEN" = 'CALLER_TOKEN' || return 82
  test "$VAULT_ADDR" = 'https://caller-old.example' || return 83
}
deploy || exit "$?"
test "$VAULT_TOKEN" = 'GLOBAL_TOKEN' || exit 84
test "$VAULT_ADDR" = 'https://global-old.example' || exit 85
`
			command := exec.Command(path, "-c", commandText)
			command.Env = environmentWithOverrides(os.Environ(), map[string]string{
				"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"),
			})
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("nested activation did not fail closed: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "refusing nested shell activation") {
				t.Fatalf("nested activation did not explain the safe alternative: %s", output)
			}
		})
	}
}

func TestRenderedShellPlansFailFastAndCommitMetadataLast(t *testing.T) {
	t.Parallel()

	context := config.Context{Address: "https://vault.example", Namespace: "admin"}
	for _, shell := range []string{"sh", "fish", "powershell"} {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			script, err := Script(context, shell, false, "prod", "stage", "sha256:"+strings.Repeat("d", 64))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(script, "VAULT_CACERT_BYTES") {
				t.Fatalf("%s plan does not clear VAULT_CACERT_BYTES:\n%s", shell, script)
			}
			metadataClear := strings.Index(script, "VAULTCTX_CONTEXT")
			connectionSet := strings.Index(script, "VAULT_ADDR")
			metadataSet := strings.LastIndex(script, "VAULTCTX_CONTEXT")
			if metadataClear < 0 || connectionSet < 0 || metadataSet <= metadataClear || metadataClear > connectionSet || connectionSet > metadataSet {
				t.Fatalf("%s plan does not clear metadata first and set it last:\n%s", shell, script)
			}

			lines := strings.Split(strings.TrimSuffix(script, "\n"), "\n")
			switch shell {
			case "sh":
				for index, line := range lines[:len(lines)-1] {
					if !strings.HasSuffix(line, " &&") {
						t.Errorf("POSIX operation %d is not fail-fast chained: %q", index, line)
					}
				}
				if strings.HasSuffix(lines[len(lines)-1], " &&") {
					t.Errorf("final POSIX operation has a dangling chain: %q", lines[len(lines)-1])
				}
			case "fish":
				for _, fragment := range []string{"begin\n", "  set -l _vaultctx_apply_status 1\n", "  while true\n", "    set -gx VAULTCTX_CONTEXT ''\n", "    set -x VAULTCTX_CONTEXT ''\n", "    set -gx VAULT_ADDR 'https://vault.example'\n", "    set -x VAULT_ADDR 'https://vault.example'\n", "    set -l _vaultctx_actual (string join -- : $VAULTCTX_CONTEXT)\n", "    set -q _vaultctx_actual[1]\n", "    or set _vaultctx_actual ''\n", "    test \"$_vaultctx_actual\" = ''\n", "    or break\n", "    set _vaultctx_apply_status 0\n", "  test \"$_vaultctx_apply_status\" -eq 0\n", "end\n"} {
					if !strings.Contains(script, fragment) {
						t.Errorf("fish plan is missing guarded status fragment %q:\n%s", fragment, script)
					}
				}
			case "powershell":
				if strings.Contains(script, "SilentlyContinue") {
					t.Errorf("PowerShell plan masks an environment error:\n%s", script)
				}
				for index, line := range lines {
					if !strings.Contains(line, "-ErrorAction Stop") {
						t.Errorf("PowerShell operation %d is not terminating-error guarded: %q", index, line)
					}
				}
			}
		})
	}
}

func TestBashShellInitRejectsReadonlyEnvironmentWithoutPartialActivation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash environment semantics are not available on Windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	init, err := ShellInit("bash")
	if err != nil {
		t.Fatal(err)
	}
	activation, err := Script(config.Context{Address: "https://new.example"}, "sh", false, "prod", "old", "sha256:"+strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	activationPath := filepath.Join(dir, "activation.sh")
	if err := os.WriteFile(activationPath, []byte(activation), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "vaultctx")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncommand cat \"$VAULTCTX_TEST_SCRIPT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		setup string
	}{
		{name: "readonly skip verify", setup: "export VAULT_SKIP_VERIFY=1\nreadonly VAULT_SKIP_VERIFY"},
		{name: "readonly address", setup: "readonly VAULT_ADDR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			commandText := init + `
export VAULT_ADDR='https://old.example'
export VAULTCTX_CONTEXT='old'
export VAULTCTX_FINGERPRINT='old-fingerprint'
` + tc.setup + `
vctx prod
test "$?" -ne 0 || exit 81
test "$VAULT_ADDR" = 'https://old.example' || exit 82
test "$VAULTCTX_CONTEXT" = 'old' || exit 83
test "$VAULTCTX_FINGERPRINT" = 'old-fingerprint' || exit 84
`
			command := exec.Command(bash, "--noprofile", "--norc", "-c", commandText)
			environment := make([]string, 0, len(os.Environ())+2)
			for _, item := range os.Environ() {
				key, _, _ := strings.Cut(item, "=")
				if key != "PATH" && key != "VAULTCTX_TEST_SCRIPT" {
					environment = append(environment, item)
				}
			}
			command.Env = append(environment,
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"VAULTCTX_TEST_SCRIPT="+activationPath,
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("readonly activation regression failed: %v\n%s", err, output)
			}
		})
	}
}

func TestFishShellInitPropagatesNativeFailureAndHandlesMissingVariables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the integration fake uses a POSIX executable")
	}
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is unavailable")
	}
	init, err := ShellInit("fish")
	if err != nil {
		t.Fatal(err)
	}
	activation, err := Script(config.Context{Address: "https://new.example", Namespace: "-q", CAPath: "/first:/second"}, "fish", false, "prod", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	activationPath := filepath.Join(dir, "activation.fish")
	if err := os.WriteFile(activationPath, []byte(activation), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "vaultctx")
	fakeProgram := "#!/bin/sh\n" +
		"if [ \"$VAULTCTX_FAKE_MODE\" = fail ]; then\n" +
		"  printf \"set -gx VAULT_ADDR 'https://must-not-apply.example'\\n\"\n" +
		"  exit 9\n" +
		"fi\n" +
		"/bin/cat \"$VAULTCTX_TEST_SCRIPT\"\n"
	if err := os.WriteFile(fake, []byte(fakeProgram), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("native failure", func(t *testing.T) {
		commandText := init + `
set -gx VAULTCTX_FAKE_MODE fail
set -gx VAULT_ADDR 'https://old.example'
vctx prod
set -l vctx_status $status
test "$vctx_status" -eq 9
and test "$VAULT_ADDR" = 'https://old.example'
`
		command := exec.Command(fish, "-c", commandText)
		command.Env = environmentWithOverrides(os.Environ(), map[string]string{
			"PATH":                          dir,
			"HOME":                          dir,
			"XDG_CONFIG_HOME":               filepath.Join(dir, "xdg-failure"),
			"VAULTCTX_TEST_SCRIPT":          activationPath,
			"VAULTCTX_FAKE_MODE":            "fail",
			"VAULTCTX_CONTEXT":              "",
			"VAULTCTX_FINGERPRINT":          "",
			"VAULTCTX_PREVIOUS":             "",
			"VAULTCTX_PREVIOUS_FINGERPRINT": "",
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fish wrapper masked native failure or evaluated failed output: %v\n%s", err, output)
		}
	})

	t.Run("missing variables", func(t *testing.T) {
		commandText := init + `
set -gx VAULTCTX_FAKE_MODE success
set -e VAULTCTX_CONTEXT
set -e VAULTCTX_FINGERPRINT
set -e VAULTCTX_PREVIOUS
set -e VAULTCTX_PREVIOUS_FINGERPRINT
vctx prod
and test "$VAULT_ADDR" = 'https://new.example'
and test "$VAULTCTX_CONTEXT" = prod
and test (string join : $VAULT_CAPATH) = '/first:/second'
and test "$VAULT_NAMESPACE" = '-q'
`
		command := exec.Command(fish, "-c", commandText)
		command.Env = environmentWithOverrides(os.Environ(), map[string]string{
			"PATH":                 dir,
			"HOME":                 dir,
			"XDG_CONFIG_HOME":      filepath.Join(dir, "xdg-missing"),
			"VAULTCTX_TEST_SCRIPT": activationPath,
			"VAULTCTX_FAKE_MODE":   "success",
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fish activation failed with normally absent variables: %v\n%s", err, output)
		}
	})

	t.Run("caller local scope", func(t *testing.T) {
		commandText := init + `
set -gx VAULTCTX_FAKE_MODE success
set -gx VAULT_ADDR 'https://global-old.example'
set -gx VAULT_TOKEN 'GLOBAL_TOKEN'
function deploy
  set -lx VAULT_ADDR 'https://caller-old.example'
  set -lx VAULT_TOKEN 'CALLER_TOKEN'
  vctx prod
  or return 81
  test "$VAULT_ADDR" = 'https://new.example'
  and test -z "$VAULT_TOKEN"
end
deploy
or exit $status
test "$VAULT_ADDR" = 'https://new.example'
and test -z "$VAULT_TOKEN"
`
		command := exec.Command(fish, "-c", commandText)
		command.Env = environmentWithOverrides(os.Environ(), map[string]string{
			"PATH":                 dir,
			"HOME":                 dir,
			"XDG_CONFIG_HOME":      filepath.Join(dir, "xdg-local"),
			"VAULTCTX_TEST_SCRIPT": activationPath,
			"VAULTCTX_FAKE_MODE":   "success",
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fish caller-local values resurfaced across activation: %v\n%s", err, output)
		}
	})

	t.Run("universal scope", func(t *testing.T) {
		commandText := init + `
set -gx VAULTCTX_FAKE_MODE success
set -Ux VAULT_ADDR 'https://universal-old.example'
set -Ux VAULT_TOKEN 'UNIVERSAL_TOKEN'
vctx prod
or exit 81
test "$VAULT_ADDR" = 'https://new.example'
and test -z "$VAULT_TOKEN"
or exit 82
set -eg VAULT_ADDR
set -eg VAULT_TOKEN
test "$VAULT_ADDR" = 'https://universal-old.example'
and test "$VAULT_TOKEN" = 'UNIVERSAL_TOKEN'
`
		command := exec.Command(fish, "-c", commandText)
		command.Env = environmentWithOverrides(os.Environ(), map[string]string{
			"PATH":                 dir,
			"HOME":                 dir,
			"XDG_CONFIG_HOME":      filepath.Join(dir, "xdg-universal"),
			"VAULTCTX_TEST_SCRIPT": activationPath,
			"VAULTCTX_FAKE_MODE":   "success",
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fish activation modified or exposed universal values: %v\n%s", err, output)
		}
	})
}

func TestPowerShellInitPropagatesNativeFailureAndAppliesSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the integration fake uses a POSIX executable")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell is unavailable")
	}
	init, err := ShellInit("powershell")
	if err != nil {
		t.Fatal(err)
	}
	activation, err := Script(config.Context{Address: "https://new.example"}, "powershell", false, "prod", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	activationPath := filepath.Join(dir, "activation.ps1")
	if err := os.WriteFile(activationPath, []byte(activation), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "vaultctx")
	fakeProgram := "#!/bin/sh\n" +
		"if [ \"$VAULTCTX_FAKE_MODE\" = fail ]; then\n" +
		"  printf \"Set-Item Env:VAULT_ADDR -Value 'https://must-not-apply.example' -ErrorAction Stop\\n\"\n" +
		"  exit 9\n" +
		"fi\n" +
		"/bin/cat \"$VAULTCTX_TEST_SCRIPT\"\n"
	if err := os.WriteFile(fake, []byte(fakeProgram), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("native failure", func(t *testing.T) {
		commandText := init + `
$Env:VAULTCTX_FAKE_MODE = 'fail'
$Env:VAULT_ADDR = 'https://old.example'
$failed = $false
try { vctx prod } catch { $failed = $true }
if (-not $failed) { exit 81 }
if ($Env:VAULT_ADDR -ne 'https://old.example') { exit 82 }
`
		command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", commandText)
		command.Env = environmentWithOverrides(os.Environ(), map[string]string{
			"PATH":                 dir,
			"VAULTCTX_TEST_SCRIPT": activationPath,
			"VAULTCTX_FAKE_MODE":   "fail",
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("PowerShell wrapper masked native failure or evaluated failed output: %v\n%s", err, output)
		}
	})

	t.Run("success", func(t *testing.T) {
		commandText := init + `
$Env:VAULTCTX_FAKE_MODE = 'success'
try { vctx prod } catch { Write-Error $_; exit 83 }
if ($Env:VAULT_ADDR -ne 'https://new.example') { exit 84 }
if ($Env:VAULTCTX_CONTEXT -ne 'prod') { exit 85 }
`
		command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", commandText)
		command.Env = environmentWithOverrides(os.Environ(), map[string]string{
			"PATH":                 dir,
			"VAULTCTX_TEST_SCRIPT": activationPath,
			"VAULTCTX_FAKE_MODE":   "success",
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("PowerShell activation failed: %v\n%s", err, output)
		}
	})
}

func environmentWithOverrides(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		replaced := false
		for override := range overrides {
			if strings.EqualFold(key, override) {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, item)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result
}

func environmentMap(t *testing.T, environment []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			t.Fatalf("invalid environment entry %q", item)
		}
		if _, duplicate := result[key]; duplicate {
			t.Fatalf("duplicate environment entry for %q in %v", key, environment)
		}
		result[key] = value
	}
	return result
}
