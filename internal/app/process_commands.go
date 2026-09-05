package app

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sohilkaushal/vaultctx/internal/config"
	"github.com/sohilkaushal/vaultctx/internal/contextenv"
)

const blockedTokenPrefix = "vaultctx-invalid-"

var errProcessCleanupIncomplete = errors.New("process cleanup incomplete")

type execOptions struct {
	name                 string
	expectContext        string
	expectContextSet     bool
	expectFingerprint    string
	expectFingerprintSet bool
	forwardAmbientToken  bool
	allowTokenHelper     bool
	command              []string
}

func (a *App) runExec(ctx context.Context, args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx exec [--expect-context NAME] [--expect-fingerprint DIGEST] [--forward-ambient-token|--allow-token-helper] [NAME|-] -- COMMAND [ARG...]\n")
		return err
	}
	options, err := parseExecArgs(args)
	if err != nil {
		return err
	}
	cfg, err := a.Store.Load()
	if err != nil {
		return err
	}
	name, selected, err := a.resolveContext(cfg, options.name)
	if err != nil {
		return err
	}
	if options.expectContextSet && options.expectContext != name {
		return fmt.Errorf("expected context %q, resolved %q", options.expectContext, name)
	}
	if options.expectFingerprintSet && options.expectFingerprint != selected.Fingerprint() {
		return fmt.Errorf("destination fingerprint mismatch: expected %q, resolved %q", options.expectFingerprint, selected.Fingerprint())
	}

	authMode := "ambient token/MFA/headers cleared; token-helper lookup fallback blocked (helper writes not isolated)"
	childEnv := contextenv.Apply(a.environment(), selected, options.forwardAmbientToken)
	childEnv = setEnvironment(childEnv, "VAULTCTX_CONTEXT", name)
	childEnv = setEnvironment(childEnv, "VAULTCTX_FINGERPRINT", selected.Fingerprint())
	if previous := a.ambient("VAULTCTX_CONTEXT"); previous != "" && previous != name {
		childEnv = setEnvironment(childEnv, "VAULTCTX_PREVIOUS", previous)
		childEnv = removeEnvironment(childEnv, "VAULTCTX_PREVIOUS_FINGERPRINT")
		if previousFingerprint := a.ambient("VAULTCTX_FINGERPRINT"); previousFingerprint != "" {
			childEnv = setEnvironment(childEnv, "VAULTCTX_PREVIOUS_FINGERPRINT", previousFingerprint)
		}
	}
	switch {
	case options.forwardAmbientToken:
		if a.ambient("VAULT_TOKEN") == "" {
			return errors.New("--forward-ambient-token requires a non-empty VAULT_TOKEN")
		}
		authMode = "ambient token explicitly forwarded; MFA/headers cleared"
	case options.allowTokenHelper:
		authMode = "Vault token-helper lookup explicitly allowed; ambient token/MFA/headers cleared"
	default:
		blockedToken, err := a.freshBlockedToken()
		if err != nil {
			return err
		}
		childEnv = setEnvironment(childEnv, "VAULT_TOKEN", blockedToken)
	}

	namespace := selected.Namespace
	if namespace == "" {
		namespace = "<root/unset>"
	}
	agent := selected.AgentAddress
	if agent == "" {
		agent = "<unset>"
	}
	proxy := selected.ProxyAddress
	if proxy == "" {
		proxy = "<unset>"
	}
	tlsServerName := selected.TLSServerName
	if tlsServerName == "" {
		tlsServerName = "<default>"
	}
	var notice strings.Builder
	fmt.Fprintf(&notice, "vaultctx: exec %q -> address=%s namespace=%s fingerprint=%s agent=%s proxy=%s tls-server-name=%s; auth=%s\n",
		name, selected.Address, namespace, selected.Fingerprint(), agent, proxy, tlsServerName, authMode)
	if selected.UsesPlainHTTP() {
		notice.WriteString("vaultctx: warning: Vault destination uses plaintext HTTP\n")
	}
	if selected.AgentUsesPlainHTTP() {
		notice.WriteString("vaultctx: warning: Vault Agent destination uses plaintext HTTP\n")
	}
	if selected.ProxyUsesPlainHTTP() {
		notice.WriteString("vaultctx: warning: Vault proxy transport uses HTTP; end-to-end Vault TLS must remain enabled\n")
	}
	if inherited := ambientTransportVariables(a.environment()); len(inherited) > 0 {
		fmt.Fprintf(&notice, "vaultctx: warning: inherited transport/trust variables may affect routing or TLS (values hidden; not fingerprinted): %s\n", strings.Join(inherited, ","))
	}
	if _, err := io.WriteString(a.Err, notice.String()); err != nil {
		return fmt.Errorf("write execution safety notice: %w", err)
	}

	path, err := a.resolveExecutable(options.command[0])
	if err != nil {
		return err
	}

	// Cancellation is managed explicitly so Unix children can be placed in a
	// dedicated process group and terminated together with their descendants.
	// Passing a non-canceling context here prevents exec.CommandContext from
	// racing that graceful group shutdown by killing only the direct child.
	cmd := a.Command(context.WithoutCancel(ctx), path, options.command[1:]...)
	cmd.Env = childEnv
	cmd.Stdin = a.In
	cmd.Stdout = a.Out
	cmd.Stderr = a.Err
	if err := runManagedCommand(ctx, cmd); err != nil {
		if errors.Is(err, errProcessCleanupIncomplete) {
			return fmt.Errorf("execute %q: %w", options.command[0], err)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &exitStatus{code: managedExitCode(exitErr)}
		}
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return ctxErr
		}
		return fmt.Errorf("execute %q: %w", options.command[0], err)
	}
	return nil
}

func (a *App) freshBlockedToken() (string, error) {
	source := a.Random
	if source == nil {
		source = cryptorand.Reader
	}
	random := make([]byte, 32)
	if _, err := io.ReadFull(source, random); err != nil {
		return "", fmt.Errorf("generate token-helper blocker: %w", err)
	}
	return blockedTokenPrefix + hex.EncodeToString(random), nil
}

func parseExecArgs(args []string) (execOptions, error) {
	var options execOptions
	delimiter := -1
	for index, arg := range args {
		if arg == "--" {
			delimiter = index
			break
		}
	}
	if delimiter < 0 {
		return options, errors.New("exec requires `--` before the command")
	}
	if delimiter == len(args)-1 {
		return options, errors.New("exec requires a command after `--`")
	}
	options.command = append([]string(nil), args[delimiter+1:]...)

	left := args[:delimiter]
	for index := 0; index < len(left); index++ {
		arg := left[index]
		switch {
		case arg == "--help" || arg == "-h":
			return options, errors.New("usage: vaultctx exec [--expect-context NAME] [--forward-ambient-token|--allow-token-helper] [NAME] -- COMMAND")
		case arg == "--forward-ambient-token":
			options.forwardAmbientToken = true
		case arg == "--allow-token-helper":
			options.allowTokenHelper = true
		case arg == "--expect-context":
			options.expectContextSet = true
			index++
			if index >= len(left) {
				return options, errors.New("--expect-context requires a value")
			}
			options.expectContext = left[index]
		case strings.HasPrefix(arg, "--expect-context="):
			options.expectContextSet = true
			options.expectContext = strings.TrimPrefix(arg, "--expect-context=")
		case arg == "--expect-fingerprint":
			options.expectFingerprintSet = true
			index++
			if index >= len(left) {
				return options, errors.New("--expect-fingerprint requires a value")
			}
			options.expectFingerprint = left[index]
		case strings.HasPrefix(arg, "--expect-fingerprint="):
			options.expectFingerprintSet = true
			options.expectFingerprint = strings.TrimPrefix(arg, "--expect-fingerprint=")
		case arg == "-":
			if options.name != "" {
				return options, errors.New("exec accepts at most one context name")
			}
			options.name = arg
		case strings.HasPrefix(arg, "-"):
			return options, fmt.Errorf("unknown exec flag %q", arg)
		default:
			if options.name != "" {
				return options, errors.New("exec accepts at most one context name")
			}
			options.name = arg
		}
	}
	if options.forwardAmbientToken && options.allowTokenHelper {
		return options, errors.New("--forward-ambient-token and --allow-token-helper are mutually exclusive")
	}
	if options.expectContextSet {
		if err := config.ValidateName(options.expectContext); err != nil {
			return options, fmt.Errorf("invalid --expect-context: %w", err)
		}
	}
	if options.expectFingerprintSet {
		if err := validateFingerprint(options.expectFingerprint); err != nil {
			return options, fmt.Errorf("invalid --expect-fingerprint: %w", err)
		}
	}
	return options, nil
}

func validateFingerprint(value string) error {
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

func (a *App) resolveExecutable(name string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		path, err := filepath.Abs(name)
		if err != nil {
			return "", fmt.Errorf("resolve executable %q: %w", name, err)
		}
		return path, nil
	}
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(name)
	if err != nil {
		return "", fmt.Errorf("find executable %q: %w", name, err)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("refusing non-absolute executable path %q", path)
	}
	return path, nil
}

func setEnvironment(base []string, key, value string) []string {
	result := make([]string, 0, len(base)+1)
	for _, item := range base {
		itemKey, _, _ := strings.Cut(item, "=")
		if !environmentKeysEqual(itemKey, key) {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

func removeEnvironment(base []string, key string) []string {
	result := make([]string, 0, len(base))
	for _, item := range base {
		itemKey, _, _ := strings.Cut(item, "=")
		if !environmentKeysEqual(itemKey, key) {
			result = append(result, item)
		}
	}
	return result
}

func environmentKeysEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func ambientTransportVariables(environment []string) []string {
	known := map[string]struct{}{
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
		"http_proxy": {}, "https_proxy": {}, "all_proxy": {}, "no_proxy": {},
		"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
		"GODEBUG": {},
	}
	present := make(map[string]struct{})
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok || value == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			for candidate := range known {
				if strings.EqualFold(key, candidate) {
					present[strings.ToUpper(candidate)] = struct{}{}
					break
				}
			}
			continue
		}
		if _, ok := known[key]; ok {
			present[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(present))
	for key := range present {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a *App) runShellInit(args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx shell-init SHELL\nSupported shells: bash, zsh, fish, powershell\n")
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: vaultctx shell-init SHELL")
	}
	script, err := contextenv.ShellInit(args[0])
	if err != nil {
		return err
	}
	_, err = io.WriteString(a.Out, script)
	return err
}

func (a *App) writeActivationWarnings(name string, selected config.Context, keepToken, checkTokenHelper, shellUnchanged bool) error {
	var warnings strings.Builder
	if shellUnchanged {
		if present := ambientManagedVaultVariables(a.environment()); len(present) > 0 {
			fmt.Fprintf(&warnings, "vaultctx: caution: saving the default will not change this shell; currently set Vault variables remain active (values hidden): %s; use vctx or vaultctx exec\n", strings.Join(present, ","))
		}
	}
	if keepToken {
		namespace := selected.Namespace
		if namespace == "" {
			namespace = "<root/unset>"
		}
		agent := selected.AgentAddress
		if agent == "" {
			agent = "<unset>"
		}
		proxy := selected.ProxyAddress
		if proxy == "" {
			proxy = "<unset>"
		}
		tlsServerName := selected.TLSServerName
		if tlsServerName == "" {
			tlsServerName = "<default>"
		}
		authMode := "no ambient VAULT_TOKEN exists to retain; helper fallback remains possible"
		if a.ambient("VAULT_TOKEN") != "" {
			authMode = "ambient VAULT_TOKEN explicitly retained; MFA/headers cleared"
		}
		fmt.Fprintf(&warnings, "vaultctx: activation %q -> address=%s namespace=%s fingerprint=%s agent=%s proxy=%s tls-server-name=%s; auth=%s\n",
			name, selected.Address, namespace, selected.Fingerprint(), agent, proxy, tlsServerName, authMode)
	}
	if selected.UsesPlainHTTP() {
		fmt.Fprintf(&warnings, "vaultctx: warning: context %q uses plaintext HTTP\n", name)
	}
	if selected.AgentUsesPlainHTTP() {
		fmt.Fprintf(&warnings, "vaultctx: warning: context %q uses a plaintext HTTP Vault Agent\n", name)
	}
	if selected.ProxyUsesPlainHTTP() {
		fmt.Fprintf(&warnings, "vaultctx: warning: context %q uses an HTTP proxy; preserve end-to-end Vault TLS\n", name)
	}
	if checkTokenHelper && a.globalTokenFileExists() {
		warnings.WriteString("vaultctx: warning: Vault may reuse the global ~/.vault-token; configure an address/namespace-aware token helper\n")
	}
	if warnings.Len() == 0 {
		return nil
	}
	if _, err := io.WriteString(a.Err, warnings.String()); err != nil {
		return fmt.Errorf("write activation safety warning: %w", err)
	}
	return nil
}

func ambientManagedVaultVariables(environment []string) []string {
	known := make(map[string]struct{}, len(contextenv.ManagedVariables)+len(contextenv.EphemeralCredentialVariables))
	for _, key := range contextenv.ManagedVariables {
		known[key] = struct{}{}
	}
	for _, key := range contextenv.EphemeralCredentialVariables {
		known[key] = struct{}{}
	}
	present := make(map[string]struct{})
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok || value == "" {
			continue
		}
		for candidate := range known {
			if environmentKeysEqual(key, candidate) {
				present[candidate] = struct{}{}
				break
			}
		}
	}
	keys := make([]string, 0, len(present))
	for key := range present {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a *App) globalTokenFileExists() bool {
	homeDir := a.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return false
	}
	if info, err := os.Stat(filepath.Join(home, ".vault-token")); err == nil && info.Mode().IsRegular() {
		return true
	}
	return false
}
