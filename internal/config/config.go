// Package config owns vaultctx's on-disk context model and validation rules.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const CurrentVersion = 1

const maxURLBytes = 4096

// File is the complete, versioned vaultctx configuration.
type File struct {
	Version             int                `json:"version"`
	Current             string             `json:"current,omitempty"`
	Previous            string             `json:"previous,omitempty"`
	PreviousFingerprint string             `json:"previous_fingerprint,omitempty"`
	Contexts            map[string]Context `json:"contexts"`
}

// Context contains connection metadata understood by the Vault CLI. The schema
// has no credential fields; callers must not put secrets in free-form metadata.
type Context struct {
	Address       string `json:"address"`
	Namespace     string `json:"namespace,omitempty"`
	CACert        string `json:"ca_cert,omitempty"`
	CAPath        string `json:"ca_path,omitempty"`
	ClientCert    string `json:"client_cert,omitempty"`
	ClientKey     string `json:"client_key,omitempty"`
	TLSServerName string `json:"tls_server_name,omitempty"`
	AgentAddress  string `json:"agent_address,omitempty"`
	ProxyAddress  string `json:"proxy_address,omitempty"`
	Description   string `json:"description,omitempty"`
}

// New returns an empty, valid configuration.
func New() *File {
	return &File{Version: CurrentVersion, Contexts: make(map[string]Context)}
}

// DefaultPath returns the platform-appropriate configuration file path.
func DefaultPath() (string, error) {
	if override := os.Getenv("VAULTCTX_CONFIG"); override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("VAULTCTX_CONFIG must be an absolute path")
		}
		return filepath.Clean(override), nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(dir, "vaultctx", "config.json"), nil
}

// ValidateName validates names before they are used as context identifiers or
// passed to an interactive selector.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("context name must not be empty")
	}
	if len(name) > 64 {
		return errors.New("context name must not exceed 64 bytes")
	}
	for index := 0; index < len(name); index++ {
		char := name[index]
		alphaNumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if alphaNumeric || index > 0 && (char == '.' || char == '_' || char == '-') {
			continue
		}
		return fmt.Errorf("invalid context name %q (use ASCII letters, digits, '.', '_', or '-', starting with a letter or digit)", name)
	}
	return nil
}

// Validate checks the complete configuration for structural errors.
func (f *File) Validate() error {
	if f.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d (expected %d)", f.Version, CurrentVersion)
	}
	if f.Contexts == nil {
		return errors.New("contexts must be an object")
	}
	if len(f.Contexts) > 256 {
		return errors.New("config must not contain more than 256 contexts")
	}
	for name, context := range f.Contexts {
		if err := ValidateName(name); err != nil {
			return err
		}
		if err := context.Validate(); err != nil {
			return fmt.Errorf("context %q: %w", name, err)
		}
	}
	if f.Current != "" {
		if _, ok := f.Contexts[f.Current]; !ok {
			return fmt.Errorf("current context %q does not exist", f.Current)
		}
	}
	if f.Previous != "" {
		if _, ok := f.Contexts[f.Previous]; !ok {
			return fmt.Errorf("previous context %q does not exist", f.Previous)
		}
		if f.PreviousFingerprint != "" {
			if err := validateStoredFingerprint(f.PreviousFingerprint); err != nil {
				return fmt.Errorf("previous fingerprint: %w", err)
			}
		}
	} else if f.PreviousFingerprint != "" {
		return errors.New("previous_fingerprint requires a previous context")
	}
	return nil
}

func validateStoredFingerprint(value string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return errors.New("must start with sha256:")
	}
	digest := strings.TrimPrefix(value, prefix)
	if len(digest) != 64 || digest != strings.ToLower(digest) {
		return errors.New("must contain exactly 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("must contain exactly 64 lowercase hexadecimal characters")
	}
	return nil
}

// Validate checks connection metadata without performing network or filesystem
// access. Doctor performs those environment-dependent checks separately.
func (c Context) Validate() error {
	if err := validateHTTPURL("address", c.Address, true); err != nil {
		return err
	}
	if err := validateLoopbackHTTP("address", c.Address); err != nil {
		return err
	}
	if c.AgentAddress != "" {
		if err := validateHTTPURL("agent address", c.AgentAddress, false); err != nil {
			return err
		}
		if err := validateLoopbackHTTP("agent address", c.AgentAddress); err != nil {
			return err
		}
	}
	if c.ProxyAddress != "" {
		if err := validateHTTPURL("proxy address", c.ProxyAddress, false); err != nil {
			return err
		}
	}
	if c.ProxyAddress != "" && (c.UsesPlainHTTP() || c.AgentUsesPlainHTTP()) {
		return errors.New("proxy_address cannot be combined with a plaintext Vault or Vault Agent address")
	}
	if c.CACert != "" && c.CAPath != "" {
		return errors.New("ca_cert and ca_path are mutually exclusive")
	}
	if (c.ClientCert == "") != (c.ClientKey == "") {
		return errors.New("client_cert and client_key must be set together")
	}
	for label, value := range map[string]string{
		"namespace":       c.Namespace,
		"ca_cert":         c.CACert,
		"ca_path":         c.CAPath,
		"client_cert":     c.ClientCert,
		"client_key":      c.ClientKey,
		"tls_server_name": c.TLSServerName,
		"description":     c.Description,
	} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%s must contain valid UTF-8", label)
		}
		if strings.ContainsFunc(value, unicode.IsControl) {
			return fmt.Errorf("%s must not contain control characters", label)
		}
		if strings.ContainsFunc(value, func(r rune) bool { return !unicode.IsPrint(r) || unicode.In(r, unicode.Cf) }) {
			return fmt.Errorf("%s must not contain non-printable or Unicode format characters", label)
		}
		if len(value) > 4096 {
			return fmt.Errorf("%s must not exceed 4096 bytes", label)
		}
	}
	if len(c.Namespace) > 256 {
		return errors.New("namespace must not exceed 256 bytes")
	}
	if len(c.Description) > 512 {
		return errors.New("description must not exceed 512 bytes")
	}
	if strings.ContainsAny(c.TLSServerName, "/\\") || strings.ContainsFunc(c.TLSServerName, unicode.IsSpace) {
		return errors.New("tls_server_name must be a hostname without spaces or slashes")
	}
	for label, path := range map[string]string{
		"ca_cert": c.CACert, "ca_path": c.CAPath,
		"client_cert": c.ClientCert, "client_key": c.ClientKey,
	} {
		if path != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", label)
		}
	}
	return nil
}

// UsesPlainHTTP reports whether the context sends traffic without TLS.
func (c Context) UsesPlainHTTP() bool {
	return usesPlainHTTP(c.Address)
}

// AgentUsesPlainHTTP reports whether the configured Vault Agent uses plaintext.
func (c Context) AgentUsesPlainHTTP() bool {
	return usesPlainHTTP(c.AgentAddress)
}

// ProxyUsesPlainHTTP reports whether the configured Vault-specific proxy uses plaintext.
func (c Context) ProxyUsesPlainHTTP() bool {
	return usesPlainHTTP(c.ProxyAddress)
}

// Fingerprint binds automation to stored connection metadata rather than a
// mutable friendly name. TLS paths are hashed, not file contents; description
// is intentionally excluded.
func (c Context) Fingerprint() string {
	identity := struct {
		Address       string `json:"address"`
		Namespace     string `json:"namespace"`
		CACert        string `json:"ca_cert"`
		CAPath        string `json:"ca_path"`
		ClientCert    string `json:"client_cert"`
		ClientKey     string `json:"client_key"`
		TLSServerName string `json:"tls_server_name"`
		AgentAddress  string `json:"agent_address"`
		ProxyAddress  string `json:"proxy_address"`
	}{
		Address: c.Address, Namespace: c.Namespace, CACert: c.CACert,
		CAPath: c.CAPath, ClientCert: c.ClientCert, ClientKey: c.ClientKey,
		TLSServerName: c.TLSServerName, AgentAddress: c.AgentAddress,
		ProxyAddress: c.ProxyAddress,
	}
	encoded, _ := json.Marshal(identity)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validateHTTPURL(label, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must contain valid UTF-8", label)
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%s must not contain control characters", label)
	}
	if strings.ContainsFunc(value, func(r rune) bool { return !unicode.IsPrint(r) || unicode.In(r, unicode.Cf) }) {
		return fmt.Errorf("%s must not contain non-printable or Unicode format characters", label)
	}
	if len(value) > maxURLBytes {
		return fmt.Errorf("%s must not exceed %d bytes", label, maxURLBytes)
	}
	u, err := url.Parse(value)
	if err != nil {
		// net/url parse errors can embed the complete input, including malformed
		// user information. Keep diagnostics useful without reflecting it.
		return fmt.Errorf("invalid %s URL syntax", label)
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return fmt.Errorf("%s must use http or https", label)
	}
	if u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("%s must include a host", label)
	}
	if u.User != nil {
		return fmt.Errorf("%s must not include user information", label)
	}
	if u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s must not include an opaque value, query, or fragment", label)
	}
	if u.Path != "" && u.Path != "/" || u.RawPath != "" {
		return fmt.Errorf("%s must not include a non-root path", label)
	}
	if strings.ContainsFunc(u.Hostname(), func(r rune) bool { return r > unicode.MaxASCII }) {
		return fmt.Errorf("%s hostname must be ASCII (use its punycode form explicitly)", label)
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("%s has invalid port", label)
		}
	}
	return nil
}

func validateLoopbackHTTP(label, value string) error {
	if !usesPlainHTTP(value) {
		return nil
	}
	u, _ := url.Parse(value)
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%s using HTTP must use localhost or a literal loopback IP", label)
	}
	return nil
}

func usesPlainHTTP(value string) bool {
	u, err := url.Parse(value)
	return err == nil && strings.EqualFold(u.Scheme, "http")
}

// PermissionError reports unsafe POSIX permissions. Windows ACLs are outside
// os.FileMode's model and are therefore not evaluated here.
func PermissionError(path string, mode os.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if mode.Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions are %04o; run chmod 600 %q", path, mode.Perm(), path)
	}
	return nil
}
