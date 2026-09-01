package selector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

func TestNewWiresProcessDefaults(t *testing.T) {
	in := strings.NewReader("input")
	var out strings.Builder
	var errOut strings.Builder

	selector := New(in, &out, &errOut, true)
	if selector.In != in || selector.Out != &out || selector.Err != &errOut {
		t.Fatal("New() did not preserve the supplied terminal streams")
	}
	if !selector.Interactive {
		t.Fatal("New() did not preserve interactive mode")
	}
	if selector.LookPath == nil || selector.Command == nil {
		t.Fatal("New() did not install process execution functions")
	}
	if !reflect.DeepEqual(selector.Environment, os.Environ()) {
		t.Fatal("New() did not snapshot the process environment")
	}
}

func TestSelectRejectsNonInteractiveUse(t *testing.T) {
	lookedUp := false
	selector := &Selector{
		In:          strings.NewReader("1\n"),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: false,
		LookPath: func(string) (string, error) {
			lookedUp = true
			return "", exec.ErrNotFound
		},
	}

	selected, err := selector.Select(context.Background(), map[string]config.Context{
		"production": {Address: "https://vault.example.com"},
	}, "")
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if err == nil || err.Error() != "context name is required when no interactive terminal is available" {
		t.Fatalf("Select() error = %v, want non-interactive error", err)
	}
	if lookedUp {
		t.Fatal("Select() looked for fzf in non-interactive mode")
	}
}

func TestSelectRejectsEmptyContextSet(t *testing.T) {
	selector := &Selector{
		In:          strings.NewReader(""),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(string) (string, error) {
			t.Fatal("Select() must not look for fzf when there are no contexts")
			return "", exec.ErrNotFound
		},
	}

	selected, err := selector.Select(context.Background(), nil, "")
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if err == nil || err.Error() != "no contexts configured; add one with `vaultctx add`" {
		t.Fatalf("Select() error = %v, want empty-context error", err)
	}
}

func TestSelectReturnsOnlyContextWithoutPrompting(t *testing.T) {
	selector := &Selector{
		In:          strings.NewReader(""),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(string) (string, error) {
			t.Fatal("Select() must not look for fzf when there is only one context")
			return "", exec.ErrNotFound
		},
	}

	selected, err := selector.Select(context.Background(), map[string]config.Context{
		"only": {Address: "https://only.example.com"},
	}, "")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected != "only" {
		t.Fatalf("Select() = %q, want %q", selected, "only")
	}
}

func TestSelectNumberedFallback(t *testing.T) {
	var prompt strings.Builder
	selector := &Selector{
		In:          strings.NewReader("2\n"),
		Out:         io.Discard,
		Err:         &prompt,
		Interactive: true,
		LookPath: func(name string) (string, error) {
			if name != "fzf" {
				t.Fatalf("LookPath() name = %q, want fzf", name)
			}
			return "", exec.ErrNotFound
		},
	}
	contexts := map[string]config.Context{
		"gamma": {Address: "https://gamma.example.com"},
		"alpha": {Address: "https://alpha.example.com"},
		"beta":  {Address: "https://beta.example.com"},
	}

	selected, err := selector.Select(context.Background(), contexts, "beta")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected != "beta" {
		t.Fatalf("Select() = %q, want beta", selected)
	}

	wantPrompt := "Select a Vault context:\n" +
		"  1)   alpha  https://alpha.example.com\n" +
		"  2) * beta  https://beta.example.com\n" +
		"  3)   gamma  https://gamma.example.com\n" +
		"> "
	if prompt.String() != wantPrompt {
		t.Fatalf("numbered prompt mismatch\n got: %q\nwant: %q", prompt.String(), wantPrompt)
	}
}

func TestSelectNumberedFallbackAcceptsSelectionAtEOF(t *testing.T) {
	selector := numberedSelector(t, "1")
	contexts := map[string]config.Context{
		"second": {Address: "https://second.example.com"},
		"first":  {Address: "https://first.example.com"},
	}

	selected, err := selector.Select(context.Background(), contexts, "")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected != "first" {
		t.Fatalf("Select() = %q, want first", selected)
	}
}

func TestSelectNumberedFallbackErrors(t *testing.T) {
	contexts := map[string]config.Context{
		"alpha": {Address: "https://alpha.example.com"},
		"beta":  {Address: "https://beta.example.com"},
	}

	tests := []struct {
		name      string
		input     string
		wantError string
		cancelled bool
	}{
		{name: "blank", input: "\n", cancelled: true},
		{name: "empty EOF", input: "", cancelled: true},
		{name: "not a number", input: "alpha\n", wantError: `invalid selection "alpha"`},
		{name: "zero", input: "0\n", wantError: `invalid selection "0"`},
		{name: "too large", input: "3\n", wantError: `invalid selection "3"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selector := numberedSelector(t, test.input)
			selected, err := selector.Select(context.Background(), contexts, "")
			if selected != "" {
				t.Fatalf("Select() returned %q, want no selection", selected)
			}
			if test.cancelled {
				if !errors.Is(err, ErrCancelled) {
					t.Fatalf("Select() error = %v, want ErrCancelled", err)
				}
				return
			}
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("Select() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestSelectNumberedFallbackWrapsReadError(t *testing.T) {
	readErr := errors.New("terminal disconnected")
	selector := &Selector{
		In:          errorReader{err: readErr},
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	}

	selected, err := selector.Select(context.Background(), twoContexts(), "")
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("Select() error = %v, want wrapped read error", err)
	}
	if err == nil || !strings.Contains(err.Error(), "read selection") {
		t.Fatalf("Select() error = %v, want read-selection context", err)
	}
}

func TestSelectNumberedFallbackUnblocksOnContextCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	selector := &Selector{
		In: blockingReader{
			started: started,
			release: release,
		},
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelledAfterReadStarted := make(chan bool, 1)
	go func() {
		select {
		case <-started:
			cancelledAfterReadStarted <- true
		case <-time.After(5 * time.Second):
			cancelledAfterReadStarted <- false
		}
		cancel()
	}()

	selected, err := selector.Select(ctx, twoContexts(), "")
	if !<-cancelledAfterReadStarted {
		t.Fatal("numbered selector did not begin reading before the test timeout")
	}
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Select() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrCancelled) {
		t.Fatalf("Select() error = %v, must not be ErrCancelled", err)
	}
}

func TestSelectRejectsRelativeFZFPath(t *testing.T) {
	var commandCalled bool
	selector := &Selector{
		In:          strings.NewReader("2\n"),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(name string) (string, error) {
			if name != "fzf" {
				t.Fatalf("LookPath() name = %q, want fzf", name)
			}
			return filepath.Join("untrusted", "fzf"), nil
		},
		Command: func(context.Context, string, ...string) *exec.Cmd {
			commandCalled = true
			return nil
		},
	}

	selected, err := selector.Select(context.Background(), twoContexts(), "")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected != "beta" {
		t.Fatalf("Select() = %q, want beta from numbered fallback", selected)
	}
	if commandCalled {
		t.Fatal("Select() executed a relative fzf path")
	}
}

func TestSelectFZFUsesNULRecordsAndIsolatedEnvironment(t *testing.T) {
	contexts := map[string]config.Context{
		"zeta": {
			Address:   "https://zeta.example.com",
			Namespace: "team-z",
		},
		"alpha": {
			Address:   "https://alpha.example.com",
			Namespace: "team-a",
		},
		"production": {
			Address: "https://prod.example.com",
		},
	}
	wantInput := "alpha\t \thttps://alpha.example.com\tteam-a\x00" +
		"production\t*\thttps://prod.example.com\t\x00" +
		"zeta\t \thttps://zeta.example.com\tteam-z\x00"
	wantOutput := "production\t*\thttps://prod.example.com\t\x00"
	spec := fzfHelperSpec{
		ExpectedInput: wantInput,
		Output:        wantOutput,
		ExpectedEnv: map[string]string{
			"TERM":     "xterm-test",
			"LANG":     "en_AU.UTF-8",
			"LC_ALL":   "C",
			"NO_COLOR": "1",
			"TMPDIR":   "/tmp/vaultctx-selector-test",
		},
		AbsentEnv: []string{
			"FZF_DEFAULT_OPTS",
			"VAULT_TOKEN",
			"VAULT_ADDR",
			"VAULT_NAMESPACE",
			"PATH",
			"HOME",
		},
	}

	fakePath := filepath.Join(t.TempDir(), "fzf")
	var gotPath string
	var gotArgs []string
	selector := &Selector{
		In:          strings.NewReader(""),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(name string) (string, error) {
			if name != "fzf" {
				t.Fatalf("LookPath() name = %q, want fzf", name)
			}
			return fakePath, nil
		},
		Command: func(ctx context.Context, path string, args ...string) *exec.Cmd {
			gotPath = path
			gotArgs = append([]string(nil), args...)
			return fzfHelperCommand(t, ctx, spec)
		},
		Environment: []string{
			"TERM=xterm-test",
			"LANG=en_AU.UTF-8",
			"LC_ALL=C",
			"NO_COLOR=1",
			"TMPDIR=/tmp/vaultctx-selector-test",
			"FZF_DEFAULT_OPTS=--bind=enter:execute(attacker-command)",
			"VAULT_TOKEN=hvs.super-secret",
			"VAULT_ADDR=https://attacker.example.com",
			"VAULT_NAMESPACE=secret",
			"PATH=/attacker-controlled-bin",
			"HOME=/attacker-controlled-home",
		},
	}

	selected, err := selector.Select(context.Background(), contexts, "production")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected != "production" {
		t.Fatalf("Select() = %q, want production", selected)
	}
	if gotPath != fakePath {
		t.Fatalf("Command() path = %q, want %q", gotPath, fakePath)
	}
	wantArgs := []string{
		"--height=45%",
		"--layout=reverse",
		"--border",
		"--no-multi",
		"--read0",
		"--print0",
		"--delimiter=\\t",
		"--with-nth=2,1,3,4",
		"--prompt=vaultctx> ",
		"--header=* current | name | address | namespace",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Command() args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestSelectFZFValidatesReturnedContext(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantError string
	}{
		{
			name:      "unknown context",
			output:    "attacker\t \thttps://attacker.example.com\t\x00",
			wantError: "fzf returned an unknown context",
		},
		{
			name:      "empty output",
			output:    "",
			wantError: "fzf returned an unknown context",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selector := fzfSelector(t, fzfHelperSpec{Output: test.output})
			selected, err := selector.Select(context.Background(), twoContexts(), "")
			if selected != "" {
				t.Fatalf("Select() returned %q, want no selection", selected)
			}
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("Select() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestSelectFZFRejectsOversizedOutputThroughBoundedWriter(t *testing.T) {
	selector := fzfSelector(t, fzfHelperSpec{
		Output: strings.Repeat("x", 16*1024+1),
	})
	selected, err := selector.Select(context.Background(), twoContexts(), "")
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if !errors.Is(err, errSelectionTooLarge) {
		t.Fatalf("Select() error = %v, want errSelectionTooLarge", err)
	}
}

func TestBoundedBufferEnforcesLimit(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	n, err := buffer.Write([]byte("1234"))
	if err != nil || n != 4 {
		t.Fatalf("Write() = (%d, %v), want (4, nil)", n, err)
	}

	n, err = buffer.Write([]byte("5"))
	if n != 0 || !errors.Is(err, errSelectionTooLarge) {
		t.Fatalf("oversized Write() = (%d, %v), want (0, errSelectionTooLarge)", n, err)
	}
	if got := buffer.String(); got != "1234" {
		t.Fatalf("buffer contents after rejected write = %q, want %q", got, "1234")
	}
}

func TestSelectFZFCancellationAndNoMatch(t *testing.T) {
	for _, exitCode := range []int{1, 130} {
		t.Run(fmt.Sprintf("exit %d", exitCode), func(t *testing.T) {
			selector := fzfSelector(t, fzfHelperSpec{ExitCode: exitCode})
			selected, err := selector.Select(context.Background(), twoContexts(), "")
			if selected != "" {
				t.Fatalf("Select() returned %q, want no selection", selected)
			}
			if !errors.Is(err, ErrCancelled) {
				t.Fatalf("Select() error = %v, want ErrCancelled", err)
			}
		})
	}
}

func TestSelectFZFReturnsContextCancellationForRunningChild(t *testing.T) {
	selector := fzfSelector(t, fzfHelperSpec{Block: true})
	childStarted := make(chan struct{}, 1)
	selector.Err = notifyingWriter{notify: childStarted}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelledAfterChildStarted := make(chan bool, 1)
	go func() {
		select {
		case <-childStarted:
			cancelledAfterChildStarted <- true
		case <-time.After(5 * time.Second):
			cancelledAfterChildStarted <- false
		}
		cancel()
	}()

	selected, err := selector.Select(ctx, twoContexts(), "")
	if !<-cancelledAfterChildStarted {
		t.Fatal("fake fzf child did not signal readiness before the test timeout")
	}
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Select() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrCancelled) {
		t.Fatalf("Select() error = %v, must not be ErrCancelled", err)
	}
}

func TestSelectFZFWrapsUnexpectedFailure(t *testing.T) {
	selector := fzfSelector(t, fzfHelperSpec{ExitCode: 2})
	selected, err := selector.Select(context.Background(), twoContexts(), "")
	if selected != "" {
		t.Fatalf("Select() returned %q, want no selection", selected)
	}
	if err == nil || !strings.Contains(err.Error(), "run fzf") {
		t.Fatalf("Select() error = %v, want wrapped fzf failure", err)
	}
	if errors.Is(err, ErrCancelled) {
		t.Fatalf("Select() error = %v, must not be ErrCancelled", err)
	}
}

func TestIsolatedFZFEnvironmentUsesStrictAllowlist(t *testing.T) {
	base := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=en_AU.UTF-8",
		"LC_ALL=C",
		"LC_TIME=en_AU.UTF-8",
		"NO_COLOR=1",
		"TMPDIR=/private/tmp",
		"TMP=/tmp",
		"TEMP=/var/tmp",
		"SYSTEMROOT=C:\\Windows",
		"FZF_DEFAULT_OPTS=--bind=enter:execute(attacker-command)",
		"VAULT_TOKEN=hvs.super-secret",
		"VAULT_ADDR=https://attacker.example.com",
		"PATH=/attacker-controlled-bin",
		"HOME=/attacker-controlled-home",
	}
	want := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=en_AU.UTF-8",
		"LC_ALL=C",
		"LC_TIME=en_AU.UTF-8",
		"NO_COLOR=1",
		"TMPDIR=/private/tmp",
		"TMP=/tmp",
		"TEMP=/var/tmp",
		"SYSTEMROOT=C:\\Windows",
	}

	got := isolatedFZFEnvironment(base)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("isolatedFZFEnvironment() = %#v, want %#v", got, want)
	}
}

// TestFZFHelperProcess is executed in a child copy of the test binary. The
// selector replaces the child's environment, so the helper configuration is
// carried in an argument after "--" rather than in an environment variable.
func TestFZFHelperProcess(t *testing.T) {
	const prefix = "selector-helper="
	var encoded string
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, prefix) {
			encoded = strings.TrimPrefix(arg, prefix)
			break
		}
	}
	if encoded == "" {
		return
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		helperExitf("decode helper configuration: %v", err)
	}
	var spec fzfHelperSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		helperExitf("unmarshal helper configuration: %v", err)
	}

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		helperExitf("read stdin: %v", err)
	}
	if spec.ExpectedInput != "" && string(input) != spec.ExpectedInput {
		helperExitf("stdin = %q, want %q", input, spec.ExpectedInput)
	}
	for key, want := range spec.ExpectedEnv {
		got, ok := os.LookupEnv(key)
		if !ok {
			helperExitf("expected environment variable %s is absent", key)
		}
		if got != want {
			helperExitf("environment variable %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range spec.AbsentEnv {
		if _, ok := os.LookupEnv(key); ok {
			helperExitf("forbidden environment variable %s is present", key)
		}
	}
	if spec.Block {
		if _, err := fmt.Fprintln(os.Stderr, "ready"); err != nil {
			helperExitf("signal readiness: %v", err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if _, err := io.WriteString(os.Stdout, spec.Output); err != nil {
		helperExitf("write stdout: %v", err)
	}
	// Exit directly even on success. A test binary built with -cover otherwise
	// tries to emit coverage metadata after the selector's intentionally minimal
	// environment has removed the test harness's coverage variables.
	os.Exit(spec.ExitCode)
}

type fzfHelperSpec struct {
	ExpectedInput string            `json:"expected_input,omitempty"`
	Output        string            `json:"output,omitempty"`
	ExitCode      int               `json:"exit_code,omitempty"`
	ExpectedEnv   map[string]string `json:"expected_env,omitempty"`
	AbsentEnv     []string          `json:"absent_env,omitempty"`
	Block         bool              `json:"block,omitempty"`
}

func fzfHelperCommand(t *testing.T, ctx context.Context, spec fzfHelperSpec) *exec.Cmd {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal helper configuration: %v", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return exec.CommandContext(ctx, testBinary, "-test.run=^TestFZFHelperProcess$", "--", "selector-helper="+encoded)
}

func fzfSelector(t *testing.T, spec fzfHelperSpec) *Selector {
	t.Helper()
	fakePath := filepath.Join(t.TempDir(), "fzf")
	return &Selector{
		In:          strings.NewReader(""),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(string) (string, error) {
			return fakePath, nil
		},
		Command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return fzfHelperCommand(t, ctx, spec)
		},
	}
}

func numberedSelector(t *testing.T, input string) *Selector {
	t.Helper()
	return &Selector{
		In:          strings.NewReader(input),
		Out:         io.Discard,
		Err:         io.Discard,
		Interactive: true,
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	}
}

func twoContexts() map[string]config.Context {
	return map[string]config.Context{
		"beta":  {Address: "https://beta.example.com"},
		"alpha": {Address: "https://alpha.example.com"},
	}
}

type errorReader struct {
	err error
}

type blockingReader struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (r blockingReader) Read([]byte) (int, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return 0, io.EOF
}

type notifyingWriter struct {
	notify chan<- struct{}
}

func (w notifyingWriter) Write(data []byte) (int, error) {
	select {
	case w.notify <- struct{}{}:
	default:
	}
	return len(data), nil
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func helperExitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(99)
}
