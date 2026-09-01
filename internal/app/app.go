// Package app implements the vaultctx command-line application.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

const helpText = `vaultctx safely switches connection metadata for the Vault CLI.

Usage:
  vaultctx                         Interactively select a context
  vaultctx CONTEXT                 Save the default context by name
  vaultctx add NAME [flags]        Add or replace a context
  vaultctx import NAME [flags]     Import connection metadata from VAULT_*
  vaultctx list [--json]           List contexts
  vaultctx current [--json]        Print the current context
  vaultctx fingerprint [NAME|-]    Print the destination identity digest
  vaultctx use [flags] [NAME]      Save default or emit shell activation
  vaultctx env [flags] [NAME]      Render environment activation
  vaultctx exec [flags] [NAME] -- COMMAND [ARG...]
  vaultctx delete NAME --yes       Delete a context
  vaultctx doctor [NAME]           Check local configuration
  vaultctx shell-init SHELL        Print a vctx shell function
  vaultctx version                 Print version information

Use "vaultctx COMMAND --help" for command-specific flags.

Security:
  The context schema has no credential fields. Never put secrets in metadata.
  Activation always clears VAULT_MFA/VAULT_HEADERS and clears VAULT_TOKEN unless
  --keep-token is explicit.
  The Vault CLI's default ~/.vault-token is global; see README.md before using
  the same shell with mutually untrusted Vault addresses.
`

type picker interface {
	Select(context.Context, map[string]config.Context, string) (string, error)
}

type commandContextFunc func(context.Context, string, ...string) *exec.Cmd

// App contains all runtime dependencies so commands can be tested without
// mutating a user's actual shell or configuration.
type App struct {
	In       io.Reader
	Out      io.Writer
	Err      io.Writer
	Store    *config.Store
	Picker   picker
	Getenv   func(string) string
	Environ  func() []string
	Command  commandContextFunc
	Random   io.Reader
	HomeDir  func() (string, error)
	LookPath func(string) (string, error)
	Version  string
}

// Execute runs a command and returns a process exit code.
func (a *App) Execute(ctx context.Context, args []string) int {
	err := a.run(ctx, args)
	if err == nil {
		return 0
	}
	var status *exitStatus
	if errors.As(err, &status) {
		return status.code
	}
	if errors.Is(err, context.Canceled) {
		code := cancellationExitCode(ctx)
		if code == 143 {
			fmt.Fprintln(a.Err, "vaultctx: terminated")
		} else {
			fmt.Fprintln(a.Err, "vaultctx: interrupted")
		}
		return code
	}
	fmt.Fprintf(a.Err, "vaultctx: %v\n", err)
	return 1
}

func (a *App) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.runUse(ctx, nil)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "help", "--help", "-h":
		_, err := io.WriteString(a.Out, helpText)
		return err
	case "add":
		return a.runAdd(ctx, rest)
	case "import":
		return a.runImport(ctx, rest)
	case "list", "ls":
		return a.runList(rest)
	case "current":
		return a.runCurrent(rest)
	case "fingerprint":
		return a.runFingerprint(rest)
	case "use":
		return a.runUse(ctx, rest)
	case "env":
		return a.runEnv(ctx, rest)
	case "exec":
		return a.runExec(ctx, rest)
	case "delete", "remove", "rm":
		return a.runDelete(ctx, rest)
	case "doctor":
		return a.runDoctor(rest)
	case "shell-init":
		return a.runShellInit(rest)
	case "version", "--version":
		version := a.Version
		if version == "" {
			version = "dev"
		}
		_, err := fmt.Fprintf(a.Out, "vaultctx %s\n", version)
		return err
	default:
		if strings.HasPrefix(command, "-") || len(rest) > 0 {
			return fmt.Errorf("unknown command %q", command)
		}
		return a.runUse(ctx, []string{command})
	}
}

type exitStatus struct{ code int }

func (e *exitStatus) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func selectedContext(cfg *config.File, requested string) (string, config.Context, error) {
	name := requested
	if name == "" {
		name = cfg.Current
	}
	if name == "" {
		return "", config.Context{}, errors.New("no current context; run `vaultctx use` or specify a name")
	}
	context, ok := cfg.Contexts[name]
	if !ok {
		return "", config.Context{}, fmt.Errorf("context %q does not exist", name)
	}
	return name, context, nil
}

func (a *App) activeContextName(cfg *config.File) string {
	if shellCurrent := a.ambient("VAULTCTX_CONTEXT"); shellCurrent != "" {
		if selected, ok := cfg.Contexts[shellCurrent]; ok && a.ambient("VAULTCTX_FINGERPRINT") == selected.Fingerprint() {
			return shellCurrent
		}
		return ""
	}
	return cfg.Current
}

func (a *App) resolveContext(cfg *config.File, requested string) (string, config.Context, error) {
	if requested == "-" {
		previous := a.ambient("VAULTCTX_PREVIOUS")
		previousFingerprint := a.ambient("VAULTCTX_PREVIOUS_FINGERPRINT")
		if previous == "" {
			previous = cfg.Previous
			previousFingerprint = cfg.PreviousFingerprint
		}
		if previous == "" {
			return "", config.Context{}, errors.New("no previous context")
		}
		selected, ok := cfg.Contexts[previous]
		if !ok {
			return "", config.Context{}, fmt.Errorf("previous context %q no longer exists", previous)
		}
		if previousFingerprint == "" {
			return "", config.Context{}, fmt.Errorf("previous context %q has no identity fingerprint; select it explicitly", previous)
		}
		if previousFingerprint != selected.Fingerprint() {
			return "", config.Context{}, fmt.Errorf("previous context %q changed since it was selected; select it explicitly", previous)
		}
		requested = previous
	}
	if requested == "" {
		if shellCurrent := a.ambient("VAULTCTX_CONTEXT"); shellCurrent != "" {
			selected, ok := cfg.Contexts[shellCurrent]
			if !ok {
				return "", config.Context{}, fmt.Errorf("shell context %q no longer exists; specify a context explicitly", shellCurrent)
			}
			shellFingerprint := a.ambient("VAULTCTX_FINGERPRINT")
			if shellFingerprint == "" {
				return "", config.Context{}, fmt.Errorf("shell context %q has no activation fingerprint; run `vctx %s` again", shellCurrent, shellCurrent)
			}
			if shellFingerprint != selected.Fingerprint() {
				return "", config.Context{}, fmt.Errorf("shell context %q changed since activation; run `vctx %s` again", shellCurrent, shellCurrent)
			}
			requested = shellCurrent
		} else {
			requested = cfg.Current
		}
	}
	return selectedContext(cfg, requested)
}

func (a *App) activationPrevious(cfg *config.File, activeBefore, next string) (string, string) {
	previous := a.ambient("VAULTCTX_PREVIOUS")
	fingerprint := a.ambient("VAULTCTX_PREVIOUS_FINGERPRINT")
	if previous == "" {
		previous = cfg.Previous
		fingerprint = cfg.PreviousFingerprint
	}
	if activeBefore != "" && activeBefore != next {
		previous = activeBefore
		fingerprint = cfg.Contexts[activeBefore].Fingerprint()
	}
	return previous, fingerprint
}

func (a *App) ambient(name string) string {
	if a.Getenv == nil {
		return os.Getenv(name)
	}
	return a.Getenv(name)
}

func (a *App) environment() []string {
	if a.Environ == nil {
		return os.Environ()
	}
	return a.Environ()
}

func helpRequested(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
}
