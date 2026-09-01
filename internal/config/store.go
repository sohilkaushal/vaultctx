package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const maxConfigBytes = 1 << 20

var errConfigTooLarge = fmt.Errorf("serialized config exceeds %d bytes", maxConfigBytes)

type maxBytesWriter struct {
	writer    io.Writer
	remaining int
}

func (w *maxBytesWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	allowed := len(data)
	overflow := allowed > w.remaining
	if overflow {
		allowed = w.remaining
	}
	if allowed == 0 {
		return 0, errConfigTooLarge
	}
	written, err := w.writer.Write(data[:allowed])
	w.remaining -= written
	if err != nil {
		return written, err
	}
	if written != allowed {
		return written, io.ErrShortWrite
	}
	if overflow {
		return written, errConfigTooLarge
	}
	return written, nil
}

// Store loads and atomically updates a configuration file.
type Store struct {
	Path        string
	LockTimeout time.Duration
}

// NewStore returns a Store with conservative lock defaults.
func NewStore(path string) *Store {
	return &Store{Path: path, LockTimeout: 3 * time.Second}
}

// Load reads and validates the configuration. A missing file is an empty config.
func (s *Store) Load() (*File, error) {
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		dir := filepath.Dir(s.Path)
		if _, dirErr := os.Lstat(dir); dirErr == nil {
			if err := validateConfigDirectory(dir); err != nil {
				return nil, err
			}
		} else if !errors.Is(dirErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect config directory: %w", dirErr)
		}
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect config: %w", err)
	}
	if err := validateConfigDirectory(filepath.Dir(s.Path)); err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to read symlinked config %q", s.Path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config %q is not a regular file", s.Path)
	}
	if err := PermissionError(s.Path, info.Mode()); err != nil {
		return nil, err
	}
	if err := validateOwnerAndLinks(s.Path, info, false); err != nil {
		return nil, err
	}
	if info.Size() > maxConfigBytes {
		return nil, fmt.Errorf("config %q exceeds %d bytes", s.Path, maxConfigBytes)
	}

	f, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened config: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("config %q changed while it was being opened", s.Path)
	}
	if err := PermissionError(s.Path, openedInfo.Mode()); err != nil {
		return nil, err
	}
	if err := validateOwnerAndLinks(s.Path, openedInfo, false); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("config %q exceeds %d bytes", s.Path, maxConfigBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("decode config: config must contain valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg File
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &cfg, nil
}

func validateConfigDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("config directory %q must be a real directory, not a symlink", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config directory %q permissions are %04o; run chmod 700 %q", dir, info.Mode().Perm(), dir)
	}
	return validateOwnerAndLinks(dir, info, true)
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder, "$", 0, jsonObjectFile); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

type jsonObjectKind uint8

const (
	jsonObjectUnknown jsonObjectKind = iota
	jsonObjectFile
	jsonObjectContexts
	jsonObjectContext
)

var fileJSONKeys = map[string]struct{}{
	"version": {}, "current": {}, "previous": {}, "previous_fingerprint": {}, "contexts": {},
}

var contextJSONKeys = map[string]struct{}{
	"address": {}, "namespace": {}, "ca_cert": {}, "ca_path": {},
	"client_cert": {}, "client_key": {}, "tls_server_name": {},
	"agent_address": {}, "proxy_address": {}, "description": {},
}

func scanJSONValue(decoder *json.Decoder, path string, depth int, kind jsonObjectKind) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds 64 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		seenSchemaKeys := make([]string, 0)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key at %s", path)
			}
			seen[key] = struct{}{}
			if kind == jsonObjectFile || kind == jsonObjectContext {
				for _, prior := range seenSchemaKeys {
					if strings.EqualFold(prior, key) {
						return fmt.Errorf("duplicate schema field at %s (schema keys are case-sensitive)", path)
					}
				}
				seenSchemaKeys = append(seenSchemaKeys, key)
				allowed := fileJSONKeys
				if kind == jsonObjectContext {
					allowed = contextJSONKeys
				}
				if _, ok := allowed[key]; !ok {
					return fmt.Errorf("unknown or noncanonical field at %s (schema keys are case-sensitive)", path)
				}
			}
			childKind := jsonObjectUnknown
			childPath := path + ".<value>"
			if kind == jsonObjectFile && key == "contexts" {
				childKind = jsonObjectContexts
				childPath = path + ".contexts"
			} else if kind == jsonObjectContexts {
				childKind = jsonObjectContext
				childPath = path + ".<context>"
			} else if kind == jsonObjectFile || kind == jsonObjectContext {
				childPath = path + "." + key
			}
			if err := scanJSONValue(decoder, childPath, depth+1, childKind); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index), depth+1, jsonObjectUnknown); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode config: multiple JSON values")
	}
	return fmt.Errorf("decode config: %w", err)
}

// Update serializes all read-modify-write mutations behind a process lock.
func (s *Store) Update(change func(*File) error) error {
	return s.UpdateContext(context.Background(), change)
}

// UpdateContext is Update with cancellation while waiting and before commit.
// Once atomic replacement starts, it completes to preserve file consistency.
func (s *Store) UpdateContext(ctx context.Context, change func(*File) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := s.acquireLockContext(ctx)
	if err != nil {
		return err
	}
	defer release()

	cfg, err := s.Load()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := change(cfg); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate updated config: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.save(cfg)
}

func (s *Store) save(cfg *File) error {
	dir := filepath.Dir(s.Path)
	if err := validateConfigDirectory(dir); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary config: %w", err)
	}
	// Enforce the same bound used by Load before the atomic rename. Otherwise a
	// valid mutation could write a file that vaultctx refuses to read later.
	encoder := json.NewEncoder(&maxBytesWriter{writer: tmp, remaining: maxConfigBytes})
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		if errors.Is(err, errConfigTooLarge) {
			return errConfigTooLarge
		}
		return fmt.Errorf("encode config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	committed = true
	if runtime.GOOS != "windows" {
		directory, err := os.Open(dir)
		if err != nil {
			return fmt.Errorf("open config directory for sync: %w", err)
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil && !errors.Is(syncErr, syscall.EINVAL) {
			return fmt.Errorf("sync config directory: %w", syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close config directory: %w", closeErr)
		}
	}
	return nil
}

func (s *Store) acquireLock() (func(), error) {
	return s.acquireLockContext(context.Background())
}

func (s *Store) acquireLockContext(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.Path)
	if err := prepareConfigDirectory(dir); err != nil {
		return nil, err
	}
	return acquireProcessLockContext(ctx, s.Path+".lock", s.LockTimeout)
}

func prepareConfigDirectory(dir string) error {
	_, err := os.Lstat(dir)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if created {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure new config directory: %w", err)
		}
	}
	return validateConfigDirectory(dir)
}
