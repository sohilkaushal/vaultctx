package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

type doctorReport struct {
	writer   io.Writer
	writeErr error
	warnings int
	errors   int
}

func (r *doctorReport) write(format string, values ...any) {
	if r.writeErr != nil {
		return
	}
	_, r.writeErr = fmt.Fprintf(r.writer, format, values...)
}

func (r *doctorReport) ok(format string, values ...any) {
	r.write("[OK]    "+format+"\n", values...)
}

func (r *doctorReport) warn(format string, values ...any) {
	r.warnings++
	r.write("[WARN]  "+format+"\n", values...)
}

func (r *doctorReport) fail(format string, values ...any) {
	r.errors++
	r.write("[ERROR] "+format+"\n", values...)
}

func (a *App) runDoctor(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprintln(a.Out, "Usage: vaultctx doctor [NAME]")
		return err
	}
	if len(args) > 1 {
		return errors.New("usage: vaultctx doctor [NAME]")
	}
	requested := ""
	if len(args) == 1 {
		requested = args[0]
	}
	report := &doctorReport{writer: a.Out}

	_, configStatErr := os.Lstat(a.Store.Path)
	cfg, err := a.Store.Load()
	if err != nil {
		report.fail("configuration: %v", err)
		if report.writeErr != nil {
			return fmt.Errorf("write doctor report: %w", report.writeErr)
		}
		return &exitStatus{code: 1}
	}
	if errors.Is(configStatErr, os.ErrNotExist) {
		report.warn("configuration has not been created yet: %s", a.Store.Path)
	} else {
		ok, message := configValidationStatus(runtime.GOOS, a.Store.Path)
		if ok {
			report.ok("%s", message)
		} else {
			report.warn("%s", message)
		}
	}
	if len(cfg.Contexts) == 0 {
		report.warn("no contexts configured")
	}

	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if path, err := lookPath("vault"); err != nil || !filepath.IsAbs(path) {
		report.fail("Vault CLI was not found as an absolute PATH result")
	} else {
		report.ok("Vault CLI resolved on PATH: %s (binary provenance not verified)", path)
	}
	if path, err := lookPath("fzf"); err != nil || !filepath.IsAbs(path) {
		report.warn("fzf not found; the numbered terminal picker will be used")
	} else {
		report.ok("fzf resolved on PATH: %s (binary provenance not verified)", path)
	}

	names := sortedContextNames(cfg.Contexts)
	if requested != "" {
		if _, ok := cfg.Contexts[requested]; !ok {
			report.fail("context %q does not exist", requested)
			names = nil
		} else {
			names = []string{requested}
		}
	}
	for _, name := range names {
		a.checkContext(report, name, cfg.Contexts[name])
	}

	homeDir := a.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if home, err := homeDir(); err == nil {
		tokenPath := filepath.Join(home, ".vault-token")
		if info, err := os.Stat(tokenPath); err == nil && info.Mode().IsRegular() {
			report.warn("%s is a global Vault token cache; use an address/namespace-aware token helper", tokenPath)
		}
	}
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
		"GODEBUG",
	} {
		if a.ambient(key) != "" {
			report.warn("%s is inherited from the parent shell and is outside context isolation", key)
		}
	}

	report.write("\n%d error(s), %d warning(s)\n", report.errors, report.warnings)
	if report.writeErr != nil {
		return fmt.Errorf("write doctor report: %w", report.writeErr)
	}
	if report.errors > 0 {
		return &exitStatus{code: 1}
	}
	return nil
}

func configValidationStatus(goos, path string) (bool, string) {
	switch goos {
	case "windows":
		return false, fmt.Sprintf("configuration is strict and valid, but Windows ACL ownership is not yet verified: %s", path)
	case "darwin":
		return false, fmt.Sprintf("configuration is strict and passes owner/POSIX-mode checks, but macOS extended ACLs are not yet verified: %s", path)
	case "linux":
		return true, fmt.Sprintf("configuration is strict, valid, and passes owner/POSIX-mode checks: %s", path)
	default:
		return false, fmt.Sprintf("configuration is strict and valid, but filesystem ownership and hard-link checks are not yet verified on %s: %s", goos, path)
	}
}

func (a *App) checkContext(report *doctorReport, name string, c config.Context) {
	namespace := c.Namespace
	if namespace == "" {
		namespace = "<root/unset>"
	}
	if c.UsesPlainHTTP() {
		report.warn("context %q: plaintext address %s, namespace %s", name, c.Address, namespace)
	} else {
		report.ok("context %q: %s, namespace %s", name, c.Address, namespace)
	}
	if c.AgentUsesPlainHTTP() {
		report.warn("context %q: Vault Agent address uses plaintext HTTP", name)
	}
	checkPath := func(label, path string, wantDir bool, private bool) {
		if path == "" {
			return
		}
		kind := "regular file"
		if wantDir {
			kind = "directory"
		}
		linkInfo, err := os.Lstat(path)
		if err != nil {
			report.fail("context %q: %s %q: %v", name, label, path, err)
			return
		}
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			report.warn("context %q: %s %q is a symlink; target provenance is not verified", name, label, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			report.fail("context %q: %s target %q: %v", name, label, path, err)
			return
		}
		if wantDir && !info.IsDir() || !wantDir && !info.Mode().IsRegular() {
			report.fail("context %q: %s %q must resolve to a %s", name, label, path, kind)
			return
		}
		if private && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			report.fail("context %q: %s %q permissions %04o expose private key metadata/content", name, label, path, info.Mode().Perm())
			return
		}
		handle, err := os.Open(path)
		if err != nil {
			report.fail("context %q: %s %q is not readable: %v", name, label, path, err)
			return
		}
		if wantDir {
			_, readErr := handle.Readdirnames(1)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				_ = handle.Close()
				report.fail("context %q: %s directory %q is not readable: %v", name, label, path, readErr)
				return
			}
		}
		if err := handle.Close(); err != nil {
			report.fail("context %q: close %s %q: %v", name, label, path, err)
			return
		}
		report.ok("context %q: %s is a readable %s (content and ownership not verified)", name, label, kind)
	}
	checkPath("CA certificate", c.CACert, false, false)
	checkPath("CA path", c.CAPath, true, false)
	checkPath("client certificate", c.ClientCert, false, false)
	checkPath("client key", c.ClientKey, false, true)
	if c.ProxyUsesPlainHTTP() {
		report.warn("context %q: proxy transport is HTTP; Vault endpoint TLS must remain enabled", name)
	}
}
