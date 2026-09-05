// Package contextenv translates contexts into Vault CLI environment variables
// and shell-safe activation scripts.
package contextenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

// ManagedVariables are cleared before a context is applied. This prevents
// connection settings from the previous target from leaking into the next one.
var ManagedVariables = []string{
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
}

// EphemeralCredentialVariables are cleared by default when changing target.
// Vault's on-disk token helper is outside vaultctx's control; see README.md.
var EphemeralCredentialVariables = []string{"VAULT_TOKEN", "VAULT_MFA"}

// Variables returns the non-empty Vault variables represented by a context.
func Variables(c config.Context) map[string]string {
	values := map[string]string{
		"VAULT_ADDR":            c.Address,
		"VAULT_AGENT_ADDR":      c.AgentAddress,
		"VAULT_CACERT":          c.CACert,
		"VAULT_CAPATH":          c.CAPath,
		"VAULT_CLIENT_CERT":     c.ClientCert,
		"VAULT_CLIENT_KEY":      c.ClientKey,
		"VAULT_NAMESPACE":       c.Namespace,
		"VAULT_TLS_SERVER_NAME": c.TLSServerName,
		"VAULT_PROXY_ADDR":      c.ProxyAddress,
	}
	for key, value := range values {
		if value == "" {
			delete(values, key)
		}
	}
	return values
}

// Apply returns a child-process environment with the selected context applied.
func Apply(base []string, c config.Context, keepToken bool) []string {
	return apply(base, c, keepToken, runtime.GOOS == "windows")
}

func apply(base []string, c config.Context, keepToken, caseInsensitive bool) []string {
	remove := make(map[string]struct{}, len(ManagedVariables)+len(EphemeralCredentialVariables))
	for _, key := range ManagedVariables {
		remove[comparableEnvironmentKey(key, caseInsensitive)] = struct{}{}
	}
	for _, key := range EphemeralCredentialVariables {
		if key != "VAULT_TOKEN" || !keepToken {
			remove[comparableEnvironmentKey(key, caseInsensitive)] = struct{}{}
		}
	}

	type environmentValue struct {
		key   string
		value string
	}
	combined := make(map[string]environmentValue, len(base)+9)
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		comparable := comparableEnvironmentKey(key, caseInsensitive)
		if _, shouldRemove := remove[comparable]; !shouldRemove {
			combined[comparable] = environmentValue{key: key, value: value}
		}
	}
	for key, value := range Variables(c) {
		combined[comparableEnvironmentKey(key, caseInsensitive)] = environmentValue{key: key, value: value}
	}
	env := make([]string, 0, len(combined))
	keys := make([]string, 0, len(combined))
	for key := range combined {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := combined[key]
		env = append(env, entry.key+"="+entry.value)
	}
	return env
}

func comparableEnvironmentKey(key string, caseInsensitive bool) string {
	if caseInsensitive {
		return strings.ToUpper(key)
	}
	return key
}

// Script renders an activation program for a supported shell.
func Script(c config.Context, shell string, keepToken bool, current, previous, previousFingerprint string) (string, error) {
	shell = NormalizeShell(shell)
	if err := ValidateShell(shell); err != nil {
		return "", err
	}
	connectionValues := Variables(c)
	metadataValues := make(map[string]string, 4)
	metadataKeys := []string{"VAULTCTX_CONTEXT", "VAULTCTX_PREVIOUS", "VAULTCTX_FINGERPRINT", "VAULTCTX_PREVIOUS_FINGERPRINT"}
	clear := append([]string(nil), ManagedVariables...)
	for _, key := range EphemeralCredentialVariables {
		if key != "VAULT_TOKEN" || !keepToken {
			clear = append(clear, key)
		}
	}
	if current != "" {
		metadataValues["VAULTCTX_CONTEXT"] = current
		metadataValues["VAULTCTX_FINGERPRINT"] = c.Fingerprint()
	}
	if previous != "" {
		metadataValues["VAULTCTX_PREVIOUS"] = previous
		if previousFingerprint != "" {
			metadataValues["VAULTCTX_PREVIOUS_FINGERPRINT"] = previousFingerprint
		}
	}
	filteredClear := clear[:0]
	for _, key := range clear {
		if _, replaced := connectionValues[key]; !replaced {
			filteredClear = append(filteredClear, key)
		}
	}
	clear = filteredClear

	var b strings.Builder
	switch shell {
	case "sh":
		operations := make([]string, 0, len(metadataKeys)+len(clear)+len(connectionValues)+len(metadataValues))
		for _, key := range metadataKeys {
			operations = append(operations, "unset "+key)
		}
		for _, key := range clear {
			operations = append(operations, "unset "+key)
		}
		for _, key := range sortedKeys(connectionValues) {
			operations = append(operations, fmt.Sprintf("export %s=%s", key, quotePOSIX(connectionValues[key])))
		}
		for _, key := range sortedKeys(metadataValues) {
			operations = append(operations, fmt.Sprintf("export %s=%s", key, quotePOSIX(metadataValues[key])))
		}
		b.WriteString(strings.Join(operations, " &&\n"))
		b.WriteByte('\n')
	case "fish":
		fmt.Fprintln(&b, "begin")
		fmt.Fprintln(&b, "  set -l _vaultctx_apply_status 1")
		fmt.Fprintln(&b, "  while true")
		for _, key := range metadataKeys {
			writeFishAssignment(&b, key, "")
		}
		for _, key := range clear {
			writeFishAssignment(&b, key, "")
		}
		for _, key := range sortedKeys(connectionValues) {
			writeFishAssignment(&b, key, connectionValues[key])
		}
		for _, key := range sortedKeys(metadataValues) {
			writeFishAssignment(&b, key, metadataValues[key])
		}
		fmt.Fprintln(&b, "    set _vaultctx_apply_status 0")
		fmt.Fprintln(&b, "    break")
		fmt.Fprintln(&b, "  end")
		fmt.Fprintln(&b, "  test \"$_vaultctx_apply_status\" -eq 0")
		fmt.Fprintln(&b, "end")
	case "powershell":
		for _, key := range metadataKeys {
			fmt.Fprintf(&b, "if (Test-Path Env:%s) { Remove-Item Env:%s -ErrorAction Stop }\n", key, key)
		}
		for _, key := range clear {
			fmt.Fprintf(&b, "if (Test-Path Env:%s) { Remove-Item Env:%s -ErrorAction Stop }\n", key, key)
		}
		for _, key := range sortedKeys(connectionValues) {
			fmt.Fprintf(&b, "Set-Item Env:%s -Value %s -ErrorAction Stop\n", key, quotePowerShell(connectionValues[key]))
		}
		for _, key := range sortedKeys(metadataValues) {
			fmt.Fprintf(&b, "Set-Item Env:%s -Value %s -ErrorAction Stop\n", key, quotePowerShell(metadataValues[key]))
		}
	case "json":
		values := make(map[string]string, len(connectionValues)+len(metadataValues))
		for key, value := range connectionValues {
			values[key] = value
		}
		for key, value := range metadataValues {
			values[key] = value
		}
		jsonClear := append([]string(nil), clear...)
		for _, key := range metadataKeys {
			if _, replaced := metadataValues[key]; !replaced {
				jsonClear = append(jsonClear, key)
			}
		}
		payload := struct {
			Set   map[string]string `json:"set"`
			Unset []string          `json:"unset"`
		}{Set: values, Unset: jsonClear}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode environment: %w", err)
		}
		b.Write(encoded)
		b.WriteByte('\n')
	default:
		return "", fmt.Errorf("unsupported shell %q (use sh, fish, powershell, or json)", shell)
	}
	return b.String(), nil
}

// ValidateShell checks a renderer name without producing output.
func ValidateShell(shell string) error {
	switch NormalizeShell(shell) {
	case "sh", "fish", "powershell", "json":
		return nil
	default:
		return fmt.Errorf("unsupported shell %q (use sh, fish, powershell, or json)", shell)
	}
}

// NormalizeShell maps common executable names onto script dialects.
func NormalizeShell(shell string) string {
	shell = shellExecutableName(shell)
	switch shell {
	case "bash", "zsh", "dash", "ksh", "posix", "":
		return "sh"
	case "pwsh", "powershell.exe", "pwsh.exe":
		return "powershell"
	default:
		return shell
	}
}

// ShellInit renders a small shell function that safely evaluates use output.
func ShellInit(shell string) (string, error) {
	switch shellExecutableName(shell) {
	case "bash":
		return posixShellInit(`if [ -z "${BASH_VERSION:-}" ]; then
    printf '%s\n' 'vaultctx: this initializer was generated for Bash; regenerate it for the current shell' >&2
    return 1
  fi
  if [ "${#FUNCNAME[@]}" -gt 1 ]; then
    printf '%s\n' 'vaultctx: refusing nested shell activation; call vctx at the interactive top level or use vaultctx exec' >&2
    return 1
  fi`), nil
	case "zsh":
		return posixShellInit(`if [ -z "${ZSH_VERSION:-}" ]; then
    printf '%s\n' 'vaultctx: this initializer was generated for Zsh; regenerate it for the current shell' >&2
    return 1
  fi
  if [ "${#funcstack[@]}" -gt 1 ]; then
    printf '%s\n' 'vaultctx: refusing nested shell activation; call vctx at the interactive top level or use vaultctx exec' >&2
    return 1
  fi`), nil
	case "fish":
		return `function vctx --no-scope-shadowing
  set -l _vaultctx_env (command vaultctx use --shell=fish $argv | string collect)
  set -l _vaultctx_command_status $pipestatus[1]
  test "$_vaultctx_command_status" -eq 0
  or return "$_vaultctx_command_status"
  eval $_vaultctx_env
end
`, nil
	case "pwsh", "powershell", "powershell.exe", "pwsh.exe":
		return `function vctx {
  $vaultctxCommand = Get-Command vaultctx -CommandType Application -ErrorAction Stop
  $script = (& $vaultctxCommand.Source use --shell=powershell @args | Out-String)
  $vaultctxExitCode = $LASTEXITCODE
  if ($vaultctxExitCode -ne 0) {
    Write-Error "vaultctx exited with status $vaultctxExitCode" -ErrorAction Stop
  }
  Invoke-Expression $script -ErrorAction Stop
}
`, nil
	default:
		return "", errors.New("shell-init supports bash, zsh, fish, and powershell")
	}
}

func posixShellInit(nestedGuard string) string {
	return `vctx() {
  ` + nestedGuard + `
  _vaultctx_env="$(command vaultctx use --shell=sh "$@")" || return
  ( eval "$_vaultctx_env" )
  set -- "$?"
  if [ "$1" -ne 0 ]; then
    unset _vaultctx_env
    return "$1"
  fi
  eval "$_vaultctx_env"
  set -- "$?"
  unset _vaultctx_env
  return "$1"
}
`
}

func shellExecutableName(shell string) string {
	shell = strings.ToLower(strings.TrimSpace(shell))
	if slash := strings.LastIndexAny(shell, "/\\"); slash >= 0 {
		shell = shell[slash+1:]
	}
	return shell
}

func writeFishAssignment(b *strings.Builder, key, value string) {
	quoted := quoteFish(value)
	// A session-global binding prevents an older universal value from
	// resurfacing, while the unscoped assignment (with no-scope-shadowing on
	// vctx) updates any exported local in the caller too. Fish treats an empty
	// Vault value as unset without deleting the user's universal definition.
	fmt.Fprintf(b, "    set -gx %s %s\n", key, quoted)
	fmt.Fprintf(b, "    set -x %s %s\n", key, quoted)
	// Fish 3.7 drops an empty command substitution even when string collect is
	// passed --allow-empty. Normalize that case explicitly before comparing;
	// string join also reconstructs variables Fish represents as path lists.
	// Its option terminator keeps a leading-hyphen value from becoming a flag.
	fmt.Fprintf(b, "    set -l _vaultctx_actual (string join -- : $%s)\n", key)
	fmt.Fprintln(b, "    set -q _vaultctx_actual[1]")
	fmt.Fprintln(b, "    or set _vaultctx_actual ''")
	fmt.Fprintf(b, "    test \"$_vaultctx_actual\" = %s\n", quoted)
	fmt.Fprintln(b, "    or break")
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func quoteFish(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
