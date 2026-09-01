package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewReturnsIndependentValidFiles(t *testing.T) {
	t.Parallel()

	first := New()
	second := New()
	if err := first.Validate(); err != nil {
		t.Fatalf("New().Validate() error = %v", err)
	}
	if first.Version != CurrentVersion {
		t.Fatalf("New().Version = %d, want %d", first.Version, CurrentVersion)
	}
	first.Contexts["prod"] = Context{Address: "https://vault.example"}
	if len(second.Contexts) != 0 {
		t.Fatal("New returned files sharing the same contexts map")
	}
}

func TestValidateName(t *testing.T) {
	t.Parallel()

	valid := []string{
		"a",
		"Z9",
		"0",
		"prod.eu-west_1-primary",
		strings.Repeat("a", 64),
	}
	for _, name := range valid {
		name := name
		t.Run("valid_"+name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateName(name); err != nil {
				t.Errorf("ValidateName(%q) error = %v", name, err)
			}
		})
	}

	invalid := []struct {
		name string
		want string
	}{
		{"", "must not be empty"},
		{strings.Repeat("a", 65), "64 bytes"},
		{".prod", "invalid context name"},
		{"_prod", "invalid context name"},
		{"-prod", "invalid context name"},
		{"prod west", "invalid context name"},
		{"prod/west", "invalid context name"},
		{"prod\\west", "invalid context name"},
		{"prod;rm", "invalid context name"},
		{"prod\nexport", "invalid context name"},
		{"prod'", "invalid context name"},
		{"café", "invalid context name"},
	}
	for index, tc := range invalid {
		tc := tc
		t.Run(fmt.Sprintf("invalid_%02d", index), func(t *testing.T) {
			t.Parallel()
			err := ValidateName(tc.name)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateName(%q) error = %v, want error containing %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestFileValidate(t *testing.T) {
	t.Parallel()

	validContext := Context{Address: "https://vault.example"}
	tooMany := make(map[string]Context, 257)
	for index := 0; index < 257; index++ {
		tooMany[fmt.Sprintf("context-%03d", index)] = validContext
	}

	tests := []struct {
		name string
		file *File
		want string
	}{
		{
			name: "valid current and previous",
			file: &File{
				Version:             CurrentVersion,
				Current:             "prod",
				Previous:            "staging",
				PreviousFingerprint: validContext.Fingerprint(),
				Contexts:            map[string]Context{"prod": validContext, "staging": validContext},
			},
		},
		{name: "unsupported version", file: &File{Version: CurrentVersion + 1, Contexts: map[string]Context{}}, want: "unsupported config version"},
		{name: "nil contexts", file: &File{Version: CurrentVersion}, want: "contexts must be an object"},
		{name: "too many contexts", file: &File{Version: CurrentVersion, Contexts: tooMany}, want: "more than 256"},
		{name: "invalid context name", file: &File{Version: CurrentVersion, Contexts: map[string]Context{"bad/name": validContext}}, want: "invalid context name"},
		{name: "invalid context body", file: &File{Version: CurrentVersion, Contexts: map[string]Context{"prod": {Address: "file:///tmp/vault"}}}, want: `context "prod"`},
		{name: "missing current", file: &File{Version: CurrentVersion, Current: "missing", Contexts: map[string]Context{"prod": validContext}}, want: `current context "missing" does not exist`},
		{name: "missing previous", file: &File{Version: CurrentVersion, Previous: "missing", Contexts: map[string]Context{"prod": validContext}}, want: `previous context "missing" does not exist`},
		{name: "orphaned previous fingerprint", file: &File{Version: CurrentVersion, PreviousFingerprint: validContext.Fingerprint(), Contexts: map[string]Context{"prod": validContext}}, want: "previous_fingerprint requires"},
		{name: "malformed previous fingerprint", file: &File{Version: CurrentVersion, Previous: "prod", PreviousFingerprint: "sha256:not-valid", Contexts: map[string]Context{"prod": validContext}}, want: "previous fingerprint"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.file.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestContextValidateRejectsUnsafeMetadata(t *testing.T) {
	t.Parallel()

	absPath := filepath.Join(os.TempDir(), "vaultctx-test-material")
	valid := Context{Address: "https://vault.example"}
	tests := []struct {
		name   string
		mutate func(*Context)
		want   string
	}{
		{name: "missing address", mutate: func(c *Context) { c.Address = "" }, want: "address is required"},
		{name: "unsupported scheme", mutate: func(c *Context) { c.Address = "file:///tmp/vault" }, want: "must use http or https"},
		{name: "missing scheme", mutate: func(c *Context) { c.Address = "vault.example" }, want: "must use http or https"},
		{name: "missing host", mutate: func(c *Context) { c.Address = "https:///vault" }, want: "must include a host"},
		{name: "remote plaintext address", mutate: func(c *Context) { c.Address = "http://vault.example:8200" }, want: "must use localhost or a literal loopback IP"},
		{name: "empty hostname with port", mutate: func(c *Context) { c.Address = "https://:8200" }, want: "must include a host"},
		{name: "userinfo", mutate: func(c *Context) { c.Address = "https://admin:secret@vault.example" }, want: "must not include user information"},
		{name: "path", mutate: func(c *Context) { c.Address = "https://vault.example/v1/sys" }, want: "non-root path"},
		{name: "encoded path", mutate: func(c *Context) { c.Address = "https://vault.example/%2F" }, want: "non-root path"},
		{name: "query", mutate: func(c *Context) { c.Address = "https://vault.example?token=secret" }, want: "query, or fragment"},
		{name: "fragment", mutate: func(c *Context) { c.Address = "https://vault.example#secret" }, want: "query, or fragment"},
		{name: "control in URL", mutate: func(c *Context) { c.Address = "https://vault.example\nmalicious" }, want: "control characters"},
		{name: "address too long", mutate: func(c *Context) { c.Address = "https://" + strings.Repeat("a", maxURLBytes) }, want: "must not exceed 4096 bytes"},
		{name: "agent address too long", mutate: func(c *Context) { c.AgentAddress = "https://" + strings.Repeat("a", maxURLBytes) }, want: "must not exceed 4096 bytes"},
		{name: "proxy address too long", mutate: func(c *Context) { c.ProxyAddress = "https://" + strings.Repeat("a", maxURLBytes) }, want: "must not exceed 4096 bytes"},
		{name: "unicode hostname", mutate: func(c *Context) { c.Address = "https://münich.example" }, want: "hostname must be ASCII"},
		{name: "zero port", mutate: func(c *Context) { c.Address = "https://vault.example:0" }, want: "invalid port"},
		{name: "oversized port", mutate: func(c *Context) { c.Address = "https://vault.example:65536" }, want: "invalid port"},
		{name: "nonnumeric port", mutate: func(c *Context) { c.Address = "https://vault.example:notaport" }, want: "invalid address URL syntax"},
		{name: "bad agent scheme", mutate: func(c *Context) { c.AgentAddress = "unix:///tmp/agent" }, want: "agent address must use http or https"},
		{name: "remote plaintext agent", mutate: func(c *Context) { c.AgentAddress = "http://agent.example:8200" }, want: "must use localhost or a literal loopback IP"},
		{name: "agent userinfo", mutate: func(c *Context) { c.AgentAddress = "http://user@agent.example" }, want: "agent address must not include user information"},
		{name: "bad proxy path", mutate: func(c *Context) { c.ProxyAddress = "https://proxy.example/tunnel" }, want: "proxy address must not include a non-root path"},
		{name: "both CA sources", mutate: func(c *Context) { c.CACert, c.CAPath = absPath, absPath }, want: "mutually exclusive"},
		{name: "client cert only", mutate: func(c *Context) { c.ClientCert = absPath }, want: "must be set together"},
		{name: "client key only", mutate: func(c *Context) { c.ClientKey = absPath }, want: "must be set together"},
		{name: "control in namespace", mutate: func(c *Context) { c.Namespace = "admin\nVAULT_TOKEN=stolen" }, want: "control characters"},
		{name: "bidi override in namespace", mutate: func(c *Context) { c.Namespace = "admin\u202eevil" }, want: "Unicode format characters"},
		{name: "bidi isolate in description", mutate: func(c *Context) { c.Description = "prod\u2066evil" }, want: "Unicode format characters"},
		{name: "zero width in TLS server name", mutate: func(c *Context) { c.TLSServerName = "vault\u200b.example" }, want: "Unicode format characters"},
		{name: "namespace too long", mutate: func(c *Context) { c.Namespace = strings.Repeat("n", 257) }, want: "namespace must not exceed 256 bytes"},
		{name: "description too long", mutate: func(c *Context) { c.Description = strings.Repeat("d", 513) }, want: "description must not exceed 512 bytes"},
		{name: "generic field too long", mutate: func(c *Context) { c.CACert = "/" + strings.Repeat("x", 4096) }, want: "ca_cert must not exceed 4096 bytes"},
		{name: "TLS server name with space", mutate: func(c *Context) { c.TLSServerName = "vault internal" }, want: "hostname without spaces or slashes"},
		{name: "TLS server name with slash", mutate: func(c *Context) { c.TLSServerName = "vault/internal" }, want: "hostname without spaces or slashes"},
		{name: "TLS server name with backslash", mutate: func(c *Context) { c.TLSServerName = `vault\\internal` }, want: "hostname without spaces or slashes"},
		{name: "relative CA cert", mutate: func(c *Context) { c.CACert = "certs/ca.pem" }, want: "ca_cert must be an absolute path"},
		{name: "relative CA path", mutate: func(c *Context) { c.CAPath = "certs" }, want: "ca_path must be an absolute path"},
		{name: "relative client paths", mutate: func(c *Context) { c.ClientCert, c.ClientKey = "client.pem", "client-key.pem" }, want: "must be an absolute path"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			context := valid
			tc.mutate(&context)
			err := context.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestContextValidateRejectsInvalidUTF8InEveryStringField(t *testing.T) {
	t.Parallel()

	invalid := string([]byte{0xff})
	absPath := filepath.Join(os.TempDir(), "vaultctx-test-material")
	tests := []struct {
		name   string
		mutate func(*Context)
	}{
		{name: "address", mutate: func(c *Context) { c.Address = "https://vault.example/" + invalid }},
		{name: "agent address", mutate: func(c *Context) { c.AgentAddress = "https://agent.example/" + invalid }},
		{name: "proxy address", mutate: func(c *Context) { c.ProxyAddress = "https://proxy.example/" + invalid }},
		{name: "namespace", mutate: func(c *Context) { c.Namespace = invalid }},
		{name: "CA certificate", mutate: func(c *Context) { c.CACert = "/" + invalid }},
		{name: "CA path", mutate: func(c *Context) { c.CAPath = "/" + invalid }},
		{name: "client certificate", mutate: func(c *Context) { c.ClientCert, c.ClientKey = "/"+invalid, absPath }},
		{name: "client key", mutate: func(c *Context) { c.ClientCert, c.ClientKey = absPath, "/"+invalid }},
		{name: "TLS server name", mutate: func(c *Context) { c.TLSServerName = invalid }},
		{name: "description", mutate: func(c *Context) { c.Description = invalid }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			context := Context{Address: "https://vault.example"}
			tc.mutate(&context)
			if err := context.Validate(); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
				t.Fatalf("Validate() error = %v, want valid UTF-8 error", err)
			}
		})
	}
}

func TestURLValidationDoesNotReflectMalformedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		canary string
		value  string
		mutate func(*Context, string)
	}{
		{name: "address userinfo and escape", canary: "ADDRESS_SUPERSECRET", value: "https://admin:ADDRESS_SUPERSECRET@vault.example:%zz", mutate: func(c *Context, value string) { c.Address = value }},
		{name: "agent bad port", canary: "AGENT_SUPERSECRET", value: "https://agent.example:AGENT_SUPERSECRET", mutate: func(c *Context, value string) { c.AgentAddress = value }},
		{name: "proxy bad escape", canary: "PROXY_SUPERSECRET", value: "https://proxy.example/%PROXY_SUPERSECRET", mutate: func(c *Context, value string) { c.ProxyAddress = value }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			context := Context{Address: "https://vault.example"}
			tc.mutate(&context, tc.value)
			err := context.Validate()
			if err == nil {
				t.Fatal("Validate() accepted malformed URL")
			}
			if strings.Contains(err.Error(), tc.canary) {
				t.Fatalf("Validate() reflected malformed URL secret: %v", err)
			}
		})
	}
}

func TestContextValidateAcceptsBoundariesAndSupportedURLs(t *testing.T) {
	t.Parallel()

	absDir := os.TempDir()
	context := Context{
		Address:       "HTTPS://vault.example:65535/",
		Namespace:     strings.Repeat("n", 256),
		CACert:        filepath.Join(absDir, "ca.pem"),
		ClientCert:    filepath.Join(absDir, "client.pem"),
		ClientKey:     filepath.Join(absDir, "client-key.pem"),
		TLSServerName: "vault.internal.example",
		AgentAddress:  "https://127.0.0.1:8200/",
		ProxyAddress:  "https://[::1]:8200",
		Description:   strings.Repeat("d", 512),
	}
	if err := context.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestContextValidateAcceptsURLLengthBoundary(t *testing.T) {
	t.Parallel()
	address := "https://" + strings.Repeat("a", maxURLBytes-len("https://"))
	if len(address) != maxURLBytes {
		t.Fatal("invalid test setup")
	}
	if err := (Context{Address: address}).Validate(); err != nil {
		t.Fatalf("Validate() rejected %d-byte URL: %v", maxURLBytes, err)
	}
}

func TestUsesPlainHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		address string
		want    bool
	}{
		{"http://vault.example", true},
		{"HTTP://vault.example", true},
		{"https://vault.example", false},
		{"not a URL", false},
	}
	for _, tc := range tests {
		if got := (Context{Address: tc.address}).UsesPlainHTTP(); got != tc.want {
			t.Errorf("Context{Address: %q}.UsesPlainHTTP() = %t, want %t", tc.address, got, tc.want)
		}
	}
}

func TestContextFingerprintIsStableAndDescriptionIndependent(t *testing.T) {
	t.Parallel()

	context := Context{
		Address:       "https://vault.example",
		Namespace:     "admin",
		CACert:        "/certs/ca.pem",
		CAPath:        "/certs",
		ClientCert:    "/certs/client.pem",
		ClientKey:     "/certs/client-key.pem",
		TLSServerName: "vault.internal",
		AgentAddress:  "http://127.0.0.1:8200",
		ProxyAddress:  "https://proxy.example",
		Description:   "friendly label",
	}
	first := context.Fingerprint()
	const wantStableFingerprint = "sha256:449f16f3fa8d10918e823163eb680084db87d2928f9d29be9403966282d279a7"
	if first != wantStableFingerprint {
		t.Fatalf("Fingerprint() = %q, want stable value %q", first, wantStableFingerprint)
	}
	if second := context.Fingerprint(); second != first {
		t.Fatalf("Fingerprint() is unstable: first %q, second %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("Fingerprint() = %q, want sha256 prefix", first)
	}
	digest := strings.TrimPrefix(first, "sha256:")
	decoded, err := hex.DecodeString(digest)
	if err != nil {
		t.Fatalf("Fingerprint() digest %q is not hexadecimal: %v", digest, err)
	}
	if len(decoded) != 32 {
		t.Fatalf("Fingerprint() digest is %d bytes, want 32", len(decoded))
	}

	withNewDescription := context
	withNewDescription.Description = "renamed without changing the target"
	if got := withNewDescription.Fingerprint(); got != first {
		t.Errorf("description-only change altered fingerprint: got %q, want %q", got, first)
	}
}

func TestContextFingerprintChangesWithEveryIdentityField(t *testing.T) {
	t.Parallel()

	base := Context{
		Address:       "https://vault.example",
		Namespace:     "admin",
		CACert:        "/certs/ca.pem",
		CAPath:        "/certs",
		ClientCert:    "/certs/client.pem",
		ClientKey:     "/certs/client-key.pem",
		TLSServerName: "vault.internal",
		AgentAddress:  "http://127.0.0.1:8200",
		ProxyAddress:  "https://proxy.example",
	}
	mutations := map[string]func(*Context){
		"address":         func(c *Context) { c.Address += "/changed" },
		"namespace":       func(c *Context) { c.Namespace += "-changed" },
		"ca_cert":         func(c *Context) { c.CACert += ".changed" },
		"ca_path":         func(c *Context) { c.CAPath += "-changed" },
		"client_cert":     func(c *Context) { c.ClientCert += ".changed" },
		"client_key":      func(c *Context) { c.ClientKey += ".changed" },
		"tls_server_name": func(c *Context) { c.TLSServerName += ".changed" },
		"agent_address":   func(c *Context) { c.AgentAddress += "/changed" },
		"proxy_address":   func(c *Context) { c.ProxyAddress += "/changed" },
	}
	wantDifferentFrom := base.Fingerprint()
	for field, mutate := range mutations {
		field, mutate := field, mutate
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			changed := base
			mutate(&changed)
			if got := changed.Fingerprint(); got == wantDifferentFrom {
				t.Errorf("changing %s did not change fingerprint %q", field, got)
			}
		})
	}
}

func TestDefaultPathOverride(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "nested", "..", "config.json")
	t.Setenv("VAULTCTX_CONFIG", absolute)
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if want := filepath.Clean(absolute); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathRejectsRelativeOverride(t *testing.T) {
	t.Setenv("VAULTCTX_CONFIG", "relative/config.json")
	_, err := DefaultPath()
	if err == nil || !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("DefaultPath() error = %v, want absolute-path error", err)
	}
}

func TestPermissionError(t *testing.T) {
	t.Parallel()

	if err := PermissionError("config.json", 0o600); err != nil {
		t.Fatalf("PermissionError(0600) = %v", err)
	}
	unsafeErr := PermissionError("config.json", 0o640)
	if runtime.GOOS == "windows" {
		if unsafeErr != nil {
			t.Fatalf("PermissionError(0640) on Windows = %v", unsafeErr)
		}
		return
	}
	if unsafeErr == nil || !strings.Contains(unsafeErr.Error(), "permissions are 0640") {
		t.Fatalf("PermissionError(0640) = %v, want unsafe permissions error", unsafeErr)
	}
}
