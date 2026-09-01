package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMaxBytesWriterAllowsBoundaryAndRejectsOverflow(t *testing.T) {
	var destination bytes.Buffer
	writer := &maxBytesWriter{writer: &destination, remaining: 3}
	if written, err := writer.Write([]byte("abc")); err != nil || written != 3 {
		t.Fatalf("boundary Write() = %d, %v; want 3, nil", written, err)
	}
	if written, err := writer.Write([]byte("d")); !errors.Is(err, errConfigTooLarge) || written != 0 {
		t.Fatalf("overflow Write() = %d, %v; want 0, errConfigTooLarge", written, err)
	}
	if destination.String() != "abc" {
		t.Fatalf("destination = %q, want exact boundary content", destination.String())
	}
}

func TestStoreOversizedUpdateRollsBackReadableConfig(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "vaultctx", "config.json"))
	if err := store.Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://vault.example"}
		file.Current = "prod"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(file *File) error {
		longURL := "https://" + strings.Repeat("a", maxURLBytes-len("https://"))
		longPath := "/" + strings.Repeat("p", 4095)
		for index := 0; index < 50; index++ {
			file.Contexts[fmt.Sprintf("oversized-%02d", index)] = Context{
				Address:       longURL,
				Namespace:     strings.Repeat("n", 256),
				CACert:        longPath,
				ClientCert:    longPath,
				ClientKey:     longPath,
				TLSServerName: strings.Repeat("t", 4096),
				AgentAddress:  longURL,
				ProxyAddress:  longURL,
				Description:   strings.Repeat("d", 512),
			}
		}
		return nil
	})
	if !errors.Is(err, errConfigTooLarge) {
		t.Fatalf("oversized Update() error = %v, want errConfigTooLarge", err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("oversized update changed the previously readable config")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() after rejected oversized update: %v", err)
	}
	if loaded.Current != "prod" || len(loaded.Contexts) != 1 {
		t.Fatalf("config after rollback = %#v", loaded)
	}
}

func TestStoreAcceptsConfigAtExactSizeLimit(t *testing.T) {
	contexts := make(map[string]Context, 256)
	const baseAddress = "https://a"
	for index := 0; index < 256; index++ {
		contexts[fmt.Sprintf("context-%03d", index)] = Context{Address: baseAddress}
	}
	cfg := &File{Version: CurrentVersion, Contexts: contexts}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	remaining := maxConfigBytes - (len(encoded) + 1) // Encoder.Encode appends one newline.
	for index := 0; index < 256 && remaining > 0; index++ {
		name := fmt.Sprintf("context-%03d", index)
		selected := contexts[name]
		addition := min(remaining, maxURLBytes-len(baseAddress))
		selected.Address += strings.Repeat("a", addition)
		contexts[name] = selected
		remaining -= addition
	}
	if remaining != 0 {
		t.Fatalf("test contexts cannot fill exact limit; %d byte(s) remain", remaining)
	}
	encoded, err = json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(encoded) + 1; got != maxConfigBytes {
		t.Fatalf("test config size = %d, want %d", got, maxConfigBytes)
	}

	store := NewStore(filepath.Join(t.TempDir(), "vaultctx", "config.json"))
	if err := store.Update(func(file *File) error {
		file.Contexts = contexts
		return nil
	}); err != nil {
		t.Fatalf("Update() at exact limit: %v", err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != maxConfigBytes {
		t.Fatalf("saved size = %d, want %d", info.Size(), maxConfigBytes)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load() at exact limit: %v", err)
	}
}

func TestStoreUpdateContextCanceledBeforeCallback(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "vaultctx", "config.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := store.UpdateContext(ctx, func(*File) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateContext() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("UpdateContext called the mutation after cancellation")
	}
	if _, err := os.Stat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled update created config: %v", err)
	}
}

func TestStoreUpdateContextCancelsContendedLockBeforeCommit(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("advisory-lock cancellation is covered on supported platforms")
	}
	dir := filepath.Join(t.TempDir(), "vaultctx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(dir, "config.json"))
	store.LockTimeout = 5 * time.Second
	release, err := acquireProcessLock(store.Path+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	called := false
	go func() {
		close(started)
		result <- store.UpdateContext(ctx, func(*File) error {
			called = true
			return nil
		})
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UpdateContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UpdateContext did not unblock after cancellation")
	}
	if called {
		t.Fatal("contended canceled update called its mutation")
	}
	if _, err := os.Stat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("contended canceled update committed config: %v", err)
	}
}

const validConfigJSON = `{"version":1,"contexts":{"prod":{"address":"https://vault.example"}}}`

func TestStoreLoadMissingReturnsEmptyConfig(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "missing", "config.json"))
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != CurrentVersion || got.Current != "" || got.Previous != "" || len(got.Contexts) != 0 {
		t.Fatalf("Load() = %#v, want an empty version-%d config", got, CurrentVersion)
	}
}

func TestStoreLoadValidConfig(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfigJSON, 0o600)
	got, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != CurrentVersion || len(got.Contexts) != 1 || got.Contexts["prod"].Address != "https://vault.example" {
		t.Fatalf("Load() = %#v", got)
	}
}

func TestStoreLoadStrictJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want string
	}{
		{name: "unknown top-level field", json: `{"version":1,"contexts":{},"token":"secret"}`, want: `unknown or noncanonical field at $`},
		{name: "unknown context field", json: `{"version":1,"contexts":{"prod":{"address":"https://vault.example","token":"secret"}}}`, want: `unknown or noncanonical field at $.contexts.<context>`},
		{name: "duplicate top-level key", json: `{"version":1,"version":1,"contexts":{}}`, want: `duplicate object key at $`},
		{name: "duplicate escaped key", json: `{"version":1,"\u0076ersion":1,"contexts":{}}`, want: `duplicate object key at $`},
		{name: "duplicate context name", json: `{"version":1,"contexts":{"prod":{"address":"https://one.example"},"prod":{"address":"https://two.example"}}}`, want: `duplicate object key at $.contexts`},
		{name: "duplicate nested key", json: `{"version":1,"contexts":{"prod":{"address":"https://one.example","address":"https://two.example"}}}`, want: `duplicate object key at $.contexts.<context>`},
		{name: "noncanonical top-level key", json: `{"Version":1,"contexts":{}}`, want: `unknown or noncanonical field at $`},
		{name: "case-folded top-level duplicate", json: `{"version":999,"Version":1,"contexts":{}}`, want: `duplicate schema field at $`},
		{name: "noncanonical context key", json: `{"version":1,"contexts":{"prod":{"Address":"https://evil.example"}}}`, want: `unknown or noncanonical field at $.contexts.<context>`},
		{name: "case-folded context duplicate", json: `{"version":1,"contexts":{"prod":{"address":"https://safe.example","Address":"https://evil.example"}}}`, want: `duplicate schema field at $.contexts.<context>`},
		{name: "multiple values", json: `{"version":1,"contexts":{}} {"version":1,"contexts":{}}`, want: "unexpected trailing JSON value"},
		{name: "trailing invalid data", json: `{"version":1,"contexts":{}} trailing`, want: "invalid character"},
		{name: "truncated JSON", json: `{"version":1,"contexts":`, want: "EOF"},
		{name: "null contexts", json: `{"version":1,"contexts":null}`, want: "contexts must be an object"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeTestConfig(t, tc.json, 0o600)
			_, err := NewStore(path).Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestStoreRejectsInvalidUTF8AndPreservesPriorConfig(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "vaultctx", "config.json"))
	if err := store.Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://vault.example"}
		file.Current = "prod"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := strings.Repeat(string([]byte{0xff}), 86)
	if err := store.Update(func(file *File) error {
		context := file.Contexts["prod"]
		context.Namespace = invalid
		file.Contexts["prod"] = context
		return nil
	}); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("Update() error = %v, want valid UTF-8 rejection", err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid UTF-8 update changed the prior readable config")
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load() after rejected update: %v", err)
	}

	rawDir := t.TempDir()
	secureTestDirectory(t, rawDir)
	rawPath := filepath.Join(rawDir, "config.json")
	raw := append([]byte(`{"version":1,"contexts":{"prod":{"address":"https://vault.example","namespace":"`), 0xff)
	raw = append(raw, []byte(`"}}}`)...)
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(rawPath).Load(); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("Load(raw invalid UTF-8) error = %v, want valid UTF-8 rejection", err)
	}
}

func TestStoreParserErrorsDoNotReflectTrailingOrFieldCanaries(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`{"version":1,"contexts":{}} "CONFIG_SECRET_CANARY"`,
		`{"version":1,"contexts":{},"CONFIG_SECRET_CANARY":"value"}`,
	} {
		path := writeTestConfig(t, input, 0o600)
		_, err := NewStore(path).Load()
		if err == nil {
			t.Fatal("Load() accepted malformed config")
		}
		if strings.Contains(err.Error(), "CONFIG_SECRET_CANARY") {
			t.Fatalf("Load() reflected config canary: %v", err)
		}
	}
}

func TestRejectDuplicateJSONKeysRejectsExcessiveNesting(t *testing.T) {
	t.Parallel()

	data := []byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))
	err := rejectDuplicateJSONKeys(data)
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds 64 levels") {
		t.Fatalf("rejectDuplicateJSONKeys() error = %v, want nesting limit error", err)
	}
}

func TestStoreLoadRejectsUnsafeFileKindsAndSizes(t *testing.T) {
	t.Parallel()

	t.Run("non-regular file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		secureTestDirectory(t, dir)
		path := filepath.Join(dir, "config.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := NewStore(path).Load()
		if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Fatalf("Load() error = %v, want non-regular-file error", err)
		}
	})

	t.Run("oversized file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		secureTestDirectory(t, dir)
		path := filepath.Join(dir, "config.json")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxConfigBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = NewStore(path).Load()
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d bytes", maxConfigBytes)) {
			t.Fatalf("Load() error = %v, want size-limit error", err)
		}
	})

	t.Run("exact size limit", func(t *testing.T) {
		t.Parallel()
		padding := maxConfigBytes - len(validConfigJSON)
		path := writeTestConfig(t, validConfigJSON+strings.Repeat(" ", padding), 0o600)
		if _, err := NewStore(path).Load(); err != nil {
			t.Fatalf("Load() at exact size limit error = %v", err)
		}
	})
}

func TestStoreLoadRejectsInsecureMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	path := writeTestConfig(t, validConfigJSON, 0o600)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "permissions are 0644") {
		t.Fatalf("Load() error = %v, want unsafe-permissions error", err)
	}
}

func TestStoreLoadRejectsInsecureDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "permissions are 0755") {
		t.Fatalf("Load() error = %v, want unsafe-directory-permissions error", err)
	}
}

func TestStoreLoadRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require additional privileges on Windows")
	}
	dir := t.TempDir()
	secureTestDirectory(t, dir)
	target := filepath.Join(dir, "real-config.json")
	if err := os.WriteFile(target, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "refusing to read symlinked config") {
		t.Fatalf("Load() error = %v, want symlink error", err)
	}
}

func TestStoreLoadRejectsHardLinkedConfig(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link identity checks are implemented on Darwin and Linux")
	}
	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(dir, "second-link.json")); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "hard links; expected exactly one") {
		t.Fatalf("Load() error = %v, want hard-link error", err)
	}
}

func TestStoreUpdatePersistsMetadataAtomicallyAndPrivately(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vaultctx", "config.json")
	store := NewStore(path)
	err := store.Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://prod.example"}
		file.Contexts["staging"] = Context{Address: "https://staging.example"}
		file.Current = "prod"
		file.Previous = "staging"
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Current != "prod" || got.Previous != "staging" || len(got.Contexts) != 2 {
		t.Fatalf("Load() = %#v, want persisted current/previous metadata and two contexts", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Errorf("config mode = %04o, want 0600", gotMode)
		}
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := dirInfo.Mode().Perm(); gotMode != 0o700 {
			t.Errorf("config directory mode = %04o, want 0700", gotMode)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	wantNames := []string{filepath.Base(path)}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		wantNames = append(wantNames, filepath.Base(path)+".lock")
	}
	if strings.Join(names, "\x00") != strings.Join(wantNames, "\x00") {
		t.Fatalf("config directory entries = %v, want %v", names, wantNames)
	}
}

func TestStoreUpdateFailureDoesNotCommitAndReleasesLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	store := NewStore(path)
	sentinel := errors.New("stop mutation")
	err := store.Update(func(file *File) error {
		file.Contexts["partial"] = Context{Address: "https://partial.example"}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update() error = %v, want %v", err, sentinel)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config exists after failed initial update: %v", err)
	}
	lockInfo, lockErr := os.Stat(path + ".lock")
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if lockErr != nil {
			t.Fatalf("persistent lock file missing after failed update: %v", lockErr)
		}
		if runtime.GOOS != "windows" && lockInfo.Mode().Perm() != 0o600 {
			t.Fatalf("persistent lock mode = %04o, want 0600", lockInfo.Mode().Perm())
		}
	} else if !errors.Is(lockErr, os.ErrNotExist) {
		t.Fatalf("non-advisory lock file exists after failed update: %v", lockErr)
	}

	if err := store.Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://prod.example"}
		return nil
	}); err != nil {
		t.Fatalf("Update() after callback failure error = %v", err)
	}
	if err := store.Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://changed.example"}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("second failed Update() error = %v, want %v", err, sentinel)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if address := got.Contexts["prod"].Address; address != "https://prod.example" {
		t.Fatalf("address after failed update = %q, want original value", address)
	}
}

func TestStoreUpdateRejectsInvalidMutationWithoutCommit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	err := NewStore(path).Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://user:secret@vault.example"}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "validate updated config") {
		t.Fatalf("Update() error = %v, want validation error", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("config exists after invalid update: %v", statErr)
	}
}

func TestStoreConcurrentUpdatesDoNotLoseMutations(t *testing.T) {
	t.Parallel()

	const workers = 20
	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			store := NewStore(path)
			store.LockTimeout = 10 * time.Second
			name := fmt.Sprintf("worker-%02d", worker)
			errorsByWorker <- store.Update(func(file *File) error {
				file.Contexts[name] = Context{Address: fmt.Sprintf("https://vault-%02d.example", worker)}
				return nil
			})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent Update() error = %v", err)
		}
	}

	got, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() after concurrent updates error = %v", err)
	}
	if len(got.Contexts) != workers {
		keys := make([]string, 0, len(got.Contexts))
		for key := range got.Contexts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		t.Fatalf("context count = %d, want %d; keys = %v", len(got.Contexts), workers, keys)
	}
	for worker := 0; worker < workers; worker++ {
		name := fmt.Sprintf("worker-%02d", worker)
		wantAddress := fmt.Sprintf("https://vault-%02d.example", worker)
		if gotAddress := got.Contexts[name].Address; gotAddress != wantAddress {
			t.Errorf("context %q address = %q, want %q", name, gotAddress, wantAddress)
		}
	}
}

func TestStoreTimesOutOnHeldProcessLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	lockPath := path + ".lock"
	release, err := acquireProcessLock(lockPath, time.Second)
	if err != nil {
		t.Fatalf("acquire first process lock: %v", err)
	}
	defer release()

	store := NewStore(path)
	store.LockTimeout = 40 * time.Millisecond
	called := false
	err = store.Update(func(*File) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for config lock") {
		t.Fatalf("Update() error = %v, want lock-timeout error", err)
	}
	if called {
		t.Fatal("Update callback called while another process lock was held")
	}
}

func TestStoreWaitsForHeldProcessLockThenProceeds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	release, err := acquireProcessLock(path+".lock", time.Second)
	if err != nil {
		t.Fatalf("acquire first process lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	store := NewStore(path)
	store.LockTimeout = 2 * time.Second
	callbackStarted := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- store.Update(func(file *File) error {
			close(callbackStarted)
			file.Contexts["prod"] = Context{Address: "https://prod.example"}
			return nil
		})
	}()

	select {
	case <-callbackStarted:
		t.Fatal("Update callback started before the held lock was released")
	case err := <-finished:
		t.Fatalf("Update finished before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	released = true

	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("Update() after lock release error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Update did not proceed after process lock was released")
	}
	select {
	case <-callbackStarted:
	default:
		t.Fatal("Update completed without invoking callback")
	}
}

func TestStoreAdvisoryLockFilePersistsWithStableIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("persistent advisory lock files are used on Darwin and Linux")
	}
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vaultctx", "config.json")
	store := NewStore(path)
	if err := store.Update(func(file *File) error {
		file.Contexts["prod"] = Context{Address: "https://prod.example"}
		return nil
	}); err != nil {
		t.Fatalf("first Update() error = %v", err)
	}
	lockPath := path + ".lock"
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat persistent lock after first update: %v", err)
	}
	if before.Mode().Perm() != 0o600 {
		t.Fatalf("persistent lock mode = %04o, want 0600", before.Mode().Perm())
	}

	if err := store.Update(func(*File) error { return nil }); err != nil {
		t.Fatalf("second Update() error = %v", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat persistent lock after second update: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("persistent lock inode changed between updates")
	}
}

func TestStoreUpdateDoesNotChangeExistingInsecureDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	called := false
	err := NewStore(path).Update(func(*File) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "permissions are 0750") {
		t.Fatalf("Update() error = %v, want existing-directory permission error", err)
	}
	if called {
		t.Fatal("Update callback called for an insecure existing directory")
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("existing directory mode changed to %04o, want unchanged 0750", got)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("config created in rejected existing directory: %v", statErr)
	}
	if _, statErr := os.Stat(path + ".lock"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lock created in rejected existing directory: %v", statErr)
	}
}

func TestStoreUpdateRejectsSymlinkedConfigDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require additional privileges on Windows")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(root, "linked")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	err := NewStore(filepath.Join(symlinkDir, "config.json")).Update(func(*File) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "must be a real directory, not a symlink") {
		t.Fatalf("Update() error = %v, want symlinked-directory error", err)
	}
}

func writeTestConfig(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func secureTestDirectory(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}
