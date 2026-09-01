// Package selector provides fzf-backed and numbered terminal context pickers.
package selector

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/sohilkaushal/vaultctx/internal/config"
)

var ErrCancelled = errors.New("selection cancelled")

var errSelectionTooLarge = errors.New("fzf returned an oversized selection")

type commandContextFunc func(context.Context, string, ...string) *exec.Cmd

// Selector chooses a context without ever invoking a command shell.
type Selector struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	Interactive bool
	LookPath    func(string) (string, error)
	Command     commandContextFunc
	Environment []string
}

// New returns a selector wired to the local process environment.
func New(in io.Reader, out, errOut io.Writer, interactive bool) *Selector {
	return &Selector{
		In:          in,
		Out:         out,
		Err:         errOut,
		Interactive: interactive,
		LookPath:    exec.LookPath,
		Command:     exec.CommandContext,
		Environment: os.Environ(),
	}
}

// Select uses fzf when available and a numbered prompt otherwise.
func (s *Selector) Select(ctx context.Context, contexts map[string]config.Context, current string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(contexts) == 0 {
		return "", errors.New("no contexts configured; add one with `vaultctx add`")
	}
	if !s.Interactive {
		return "", errors.New("context name is required when no interactive terminal is available")
	}
	if len(contexts) == 1 {
		for name := range contexts {
			return name, nil
		}
	}
	if path, err := s.LookPath("fzf"); err == nil && filepath.IsAbs(path) {
		return s.selectFZF(ctx, path, contexts, current)
	}
	return s.selectNumbered(ctx, contexts, current)
}

func (s *Selector) selectFZF(ctx context.Context, path string, contexts map[string]config.Context, current string) (string, error) {
	names := sortedNames(contexts)
	var input strings.Builder
	for _, name := range names {
		marker := " "
		if name == current {
			marker = "*"
		}
		context := contexts[name]
		fmt.Fprintf(&input, "%s\t%s\t%s\t%s%c", name, marker, context.Address, context.Namespace, byte(0))
	}

	cmd := s.Command(ctx, path,
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
	)
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Env = isolatedFZFEnvironment(s.Environment)
	stdout := boundedBuffer{limit: 16 * 1024}
	cmd.Stdout = &stdout
	cmd.Stderr = s.Err
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, errSelectionTooLarge) {
			return "", errSelectionTooLarge
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130) {
			return "", ErrCancelled
		}
		return "", fmt.Errorf("run fzf: %w", err)
	}
	output := strings.TrimSuffix(stdout.String(), "\x00")
	name, _, _ := strings.Cut(output, "\t")
	if _, ok := contexts[name]; !ok {
		return "", errors.New("fzf returned an unknown context")
	}
	return name, nil
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.buffer.Len()+len(data) > b.limit {
		return 0, errSelectionTooLarge
	}
	return b.buffer.Write(data)
}

func (b *boundedBuffer) String() string { return b.buffer.String() }

func isolatedFZFEnvironment(base []string) []string {
	env := make([]string, 0, len(base))
	allowed := map[string]bool{
		"TERM": true, "COLORTERM": true, "LANG": true, "NO_COLOR": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true,
	}
	for _, item := range base {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		comparisonKey := key
		if runtime.GOOS == "windows" {
			comparisonKey = strings.ToUpper(key)
		}
		if allowed[comparisonKey] || strings.HasPrefix(comparisonKey, "LC_") {
			env = append(env, item)
		}
	}
	return env
}

func (s *Selector) selectNumbered(ctx context.Context, contexts map[string]config.Context, current string) (string, error) {
	names := sortedNames(contexts)
	fmt.Fprintln(s.Err, "Select a Vault context:")
	for index, name := range names {
		marker := " "
		if name == current {
			marker = "*"
		}
		fmt.Fprintf(s.Err, "  %d) %s %s  %s\n", index+1, marker, name, contexts[name].Address)
	}
	fmt.Fprint(s.Err, "> ")
	type readResult struct {
		line string
		err  error
	}
	result := make(chan readResult, 1)
	go func() {
		line, err := bufio.NewReader(s.In).ReadString('\n')
		result <- readResult{line: line, err: err}
	}()
	var line string
	var err error
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case read := <-result:
		line, err = read.line, read.err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read selection: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ErrCancelled
	}
	choice, err := strconv.Atoi(line)
	if err != nil || choice < 1 || choice > len(names) {
		return "", fmt.Errorf("invalid selection %q", line)
	}
	return names[choice-1], nil
}

func sortedNames(contexts map[string]config.Context) []string {
	names := make([]string, 0, len(contexts))
	for name := range contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
