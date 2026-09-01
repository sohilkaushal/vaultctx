package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sohilkaushal/vaultctx/internal/config"
	"github.com/sohilkaushal/vaultctx/internal/contextenv"
)

func (a *App) runAdd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return commandUsage(a.Out, args, `Usage: vaultctx add NAME [flags]

Flags:
  --address URL          Vault server address (required)
  --namespace NAME       Vault Enterprise/HCP namespace
  --ca-cert PATH         PEM CA certificate
  --ca-path PATH         Directory of PEM CA certificates
  --client-cert PATH     mTLS client certificate (requires --client-key)
  --client-key PATH      mTLS client key path (requires --client-cert)
  --tls-server-name NAME TLS SNI server name
  --agent-address URL    Vault Agent address
  --proxy-address URL    Vault proxy address
  --description TEXT     Non-secret operator note
  --allow-http           Acknowledge plaintext HTTP for any address
  --force                Replace an existing context
`)
	}
	name := args[0]
	fs := newFlagSet("add")
	var c config.Context
	var allowHTTP, force bool
	fs.StringVar(&c.Address, "address", "", "Vault address")
	fs.StringVar(&c.Namespace, "namespace", "", "Vault namespace")
	fs.StringVar(&c.CACert, "ca-cert", "", "CA certificate")
	fs.StringVar(&c.CAPath, "ca-path", "", "CA directory")
	fs.StringVar(&c.ClientCert, "client-cert", "", "client certificate")
	fs.StringVar(&c.ClientKey, "client-key", "", "client key")
	fs.StringVar(&c.TLSServerName, "tls-server-name", "", "TLS server name")
	fs.StringVar(&c.AgentAddress, "agent-address", "", "Vault Agent address")
	fs.StringVar(&c.ProxyAddress, "proxy-address", "", "Vault proxy address")
	fs.StringVar(&c.Description, "description", "", "description")
	fs.BoolVar(&allowHTTP, "allow-http", false, "allow plaintext HTTP")
	fs.BoolVar(&force, "force", false, "replace context")
	if err := parseFlagSet(fs, args[1:]); err != nil {
		return err
	}
	if err := config.ValidateName(name); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if contextHasHTTP(c) && !allowHTTP {
		return errors.New("plaintext HTTP requires --allow-http; use HTTPS for administrator contexts")
	}

	becameCurrent := false
	if err := a.Store.UpdateContext(ctx, func(cfg *config.File) error {
		if _, exists := cfg.Contexts[name]; exists && !force {
			return fmt.Errorf("context %q already exists (use --force to replace it)", name)
		}
		cfg.Contexts[name] = c
		if cfg.Current == "" {
			cfg.Current = name
			becameCurrent = true
		}
		return nil
	}); err != nil {
		return err
	}
	if becameCurrent {
		_, err := fmt.Fprintf(a.Out, "Added context %q and made it current.\n", name)
		return err
	}
	_, err := fmt.Fprintf(a.Out, "Added context %q.\n", name)
	return err
}

func (a *App) runImport(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return commandUsage(a.Out, args, `Usage: vaultctx import NAME [--allow-http] [--force]

Imports only the supported Vault connection variables from the current
environment. VAULT_TOKEN, VAULT_MFA, and VAULT_LICENSE are not imported.
`)
	}
	name := args[0]
	fs := newFlagSet("import")
	var allowHTTP, force bool
	fs.BoolVar(&allowHTTP, "allow-http", false, "allow plaintext HTTP")
	fs.BoolVar(&force, "force", false, "replace context")
	if err := parseFlagSet(fs, args[1:]); err != nil {
		return err
	}
	if skip := a.ambient("VAULT_SKIP_VERIFY"); skip != "" && skip != "0" && !strings.EqualFold(skip, "false") {
		return errors.New("refusing to import VAULT_SKIP_VERIFY; configure trusted CA material instead")
	}
	c := config.Context{
		Address:       a.ambient("VAULT_ADDR"),
		Namespace:     a.ambient("VAULT_NAMESPACE"),
		CACert:        a.ambient("VAULT_CACERT"),
		CAPath:        a.ambient("VAULT_CAPATH"),
		ClientCert:    a.ambient("VAULT_CLIENT_CERT"),
		ClientKey:     a.ambient("VAULT_CLIENT_KEY"),
		TLSServerName: a.ambient("VAULT_TLS_SERVER_NAME"),
		AgentAddress:  a.ambient("VAULT_AGENT_ADDR"),
		ProxyAddress:  a.ambient("VAULT_PROXY_ADDR"),
	}
	if c.ProxyAddress == "" {
		c.ProxyAddress = a.ambient("VAULT_HTTP_PROXY")
	}
	addArgs := []string{name, "--address", c.Address}
	for flagName, value := range map[string]string{
		"namespace": c.Namespace, "ca-cert": c.CACert, "ca-path": c.CAPath,
		"client-cert": c.ClientCert, "client-key": c.ClientKey,
		"tls-server-name": c.TLSServerName, "agent-address": c.AgentAddress,
		"proxy-address": c.ProxyAddress,
	} {
		if value != "" {
			addArgs = append(addArgs, "--"+flagName, value)
		}
	}
	if allowHTTP {
		addArgs = append(addArgs, "--allow-http")
	}
	if force {
		addArgs = append(addArgs, "--force")
	}
	return a.runAdd(ctx, addArgs)
}

func (a *App) runList(args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx list [--json]\n")
		return err
	}
	fs := newFlagSet("list")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "write JSON")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	cfg, err := a.Store.Load()
	if err != nil {
		return err
	}
	names := sortedContextNames(cfg.Contexts)
	active := a.activeContextName(cfg)
	if asJSON {
		type item struct {
			Name        string         `json:"name"`
			Current     bool           `json:"current"`
			Fingerprint string         `json:"fingerprint"`
			Context     config.Context `json:"context"`
		}
		items := make([]item, 0, len(names))
		for _, name := range names {
			context := cfg.Contexts[name]
			items = append(items, item{Name: name, Current: name == active, Fingerprint: context.Fingerprint(), Context: context})
		}
		encoder := json.NewEncoder(a.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(items)
	}
	if len(names) == 0 {
		_, err := fmt.Fprintln(a.Out, "No contexts configured.")
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "CURRENT\tNAME\tADDRESS\tNAMESPACE\tNOTES"); err != nil {
		return err
	}
	for _, name := range names {
		c := cfg.Contexts[name]
		marker, namespace, notes := "", c.Namespace, c.Description
		if name == active {
			marker = "*"
		}
		if namespace == "" {
			namespace = "-"
		}
		if c.UsesPlainHTTP() || c.AgentUsesPlainHTTP() || c.ProxyUsesPlainHTTP() {
			if notes != "" {
				notes += "; "
			}
			notes += "HTTP transport; run doctor"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", marker, name, c.Address, namespace, notes); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (a *App) runCurrent(args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx current [--json]\n")
		return err
	}
	fs := newFlagSet("current")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "write JSON")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	cfg, err := a.Store.Load()
	if err != nil {
		return err
	}
	name, c, err := a.resolveContext(cfg, "")
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(a.Out).Encode(struct {
			Name        string         `json:"name"`
			Fingerprint string         `json:"fingerprint"`
			Context     config.Context `json:"context"`
		}{Name: name, Fingerprint: c.Fingerprint(), Context: c})
	}
	_, err = fmt.Fprintln(a.Out, name)
	return err
}

func (a *App) runFingerprint(args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx fingerprint [NAME|-]\n")
		return err
	}
	if len(args) > 1 {
		return errors.New("usage: vaultctx fingerprint [NAME|-]")
	}
	requested := ""
	if len(args) == 1 {
		requested = args[0]
	}
	cfg, err := a.Store.Load()
	if err != nil {
		return err
	}
	_, selected, err := a.resolveContext(cfg, requested)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Out, selected.Fingerprint())
	return err
}

func (a *App) runUse(ctx context.Context, args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx use [--shell SHELL] [--keep-token] [NAME|-]\n")
		return err
	}
	options, err := parseActivationArgs("use", args)
	if err != nil {
		return err
	}
	cfg, err := a.Store.Load()
	if err != nil {
		return err
	}
	activeBefore := a.activeContextName(cfg)
	name := options.name
	if name == "" {
		name, err = a.Picker.Select(ctx, cfg.Contexts, activeBefore)
		if err != nil {
			return err
		}
	}
	name, selected, err := a.resolveContext(cfg, name)
	if err != nil {
		return err
	}
	if options.shell != "" {
		if err := contextenv.ValidateShell(options.shell); err != nil {
			return err
		}
	}
	if options.keepToken && options.shell == "" {
		return errors.New("--keep-token requires --shell because saved context selection cannot retain a shell variable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	retainsAmbientToken := options.keepToken && a.ambient("VAULT_TOKEN") != ""
	if err := a.writeActivationWarnings(name, selected, options.keepToken, options.shell != "" && !retainsAmbientToken, options.shell == ""); err != nil {
		return err
	}
	if options.shell != "" {
		// Shell activation is deliberately per-shell state. Persisted defaults
		// are changed only by a non-shell `vaultctx use`, which also avoids a
		// failed activation write leaving the saved default changed.
		previous, previousFingerprint := a.activationPrevious(cfg, activeBefore, name)
		script, err := contextenv.Script(selected, options.shell, options.keepToken, name, previous, previousFingerprint)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err = io.WriteString(a.Out, script)
		return err
	}
	if err := a.Store.UpdateContext(ctx, func(latest *config.File) error {
		latestSelected, ok := latest.Contexts[name]
		if !ok {
			return fmt.Errorf("context %q no longer exists", name)
		}
		if latestSelected.Fingerprint() != selected.Fingerprint() {
			return fmt.Errorf("context %q changed during activation; review it and retry", name)
		}
		if latest.Current != name {
			latest.Previous = latest.Current
			latest.PreviousFingerprint = ""
			if previousContext, ok := latest.Contexts[latest.Previous]; ok {
				latest.PreviousFingerprint = previousContext.Fingerprint()
			}
			latest.Current = name
		}
		return nil
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Out, "Saved default context %q; this shell is unchanged. Use vctx or vaultctx exec.\n", name)
	return err
}

func (a *App) runEnv(ctx context.Context, args []string) error {
	if helpRequested(args) {
		_, err := io.WriteString(a.Out, "Usage: vaultctx env [--shell SHELL] [--keep-token] [NAME|-]\n")
		return err
	}
	options, err := parseActivationArgs("env", args)
	if err != nil {
		return err
	}
	if options.shell == "" {
		options.shell = "sh"
	}
	cfg, err := a.Store.Load()
	if err != nil {
		return err
	}
	name, selected, err := a.resolveContext(cfg, options.name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	retainsAmbientToken := options.keepToken && a.ambient("VAULT_TOKEN") != ""
	if err := a.writeActivationWarnings(name, selected, options.keepToken, !retainsAmbientToken, false); err != nil {
		return err
	}
	activeBefore := a.activeContextName(cfg)
	previous, previousFingerprint := a.activationPrevious(cfg, activeBefore, name)
	script, err := contextenv.Script(selected, options.shell, options.keepToken, name, previous, previousFingerprint)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = io.WriteString(a.Out, script)
	return err
}

func (a *App) runDelete(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return commandUsage(a.Out, args, "Usage: vaultctx delete NAME --yes\n")
	}
	name := args[0]
	fs := newFlagSet("delete")
	var yes bool
	fs.BoolVar(&yes, "yes", false, "confirm deletion")
	if err := parseFlagSet(fs, args[1:]); err != nil {
		return err
	}
	if !yes {
		return errors.New("deletion requires --yes")
	}
	if err := a.Store.UpdateContext(ctx, func(cfg *config.File) error {
		if _, ok := cfg.Contexts[name]; !ok {
			return fmt.Errorf("context %q does not exist", name)
		}
		delete(cfg.Contexts, name)
		if cfg.Current == name {
			cfg.Current = ""
		}
		if cfg.Previous == name {
			cfg.Previous = ""
			cfg.PreviousFingerprint = ""
		}
		return nil
	}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(a.Out, "Deleted context %q.\n", name)
	return err
}

type activationOptions struct {
	name      string
	shell     string
	keepToken bool
}

func parseActivationArgs(command string, args []string) (activationOptions, error) {
	var options activationOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			return options, fmt.Errorf("usage: vaultctx %s [--shell SHELL] [--keep-token] [NAME]", command)
		case arg == "--keep-token":
			options.keepToken = true
		case arg == "--shell":
			index++
			if index >= len(args) || args[index] == "" {
				return options, errors.New("--shell requires a non-empty value")
			}
			options.shell = args[index]
		case strings.HasPrefix(arg, "--shell="):
			options.shell = strings.TrimPrefix(arg, "--shell=")
			if options.shell == "" {
				return options, errors.New("--shell requires a non-empty value")
			}
		case arg == "-":
			if options.name != "" {
				return options, errors.New("only one context name may be specified")
			}
			options.name = arg
		case strings.HasPrefix(arg, "-"):
			return options, fmt.Errorf("unknown flag %q", arg)
		default:
			if options.name != "" {
				return options, errors.New("only one context name may be specified")
			}
			options.name = arg
		}
	}
	return options, nil
}

func contextHasHTTP(c config.Context) bool {
	for _, address := range []string{c.Address, c.AgentAddress, c.ProxyAddress} {
		u, err := url.Parse(address)
		if err == nil && strings.EqualFold(u.Scheme, "http") {
			return true
		}
	}
	return false
}

func sortedContextNames(contexts map[string]config.Context) []string {
	names := make([]string, 0, len(contexts))
	for name := range contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseFlagSet(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

func commandUsage(out io.Writer, args []string, usage string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, usage)
		return err
	}
	_, err := io.WriteString(out, usage)
	if err != nil {
		return err
	}
	return errors.New("missing required arguments")
}
