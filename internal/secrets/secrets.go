// Package secrets stores the ingest token. The OS keyring is preferred; when
// none is available a 0600 file is used and the caller is told, loudly.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrNotFound is returned when a secret is absent.
var ErrNotFound = errors.New("secret not found")

// Store is a tiny secret store keyed by name.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
	// Kind describes the backend for display: "keyring", "file" or "memory".
	Kind() string
}

const service = "goldengate"

// Keyring is the OS keyring.
type Keyring struct{}

// Get implements Store.
func (Keyring) Get(name string) (string, error) {
	v, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keyring: %w", err)
	}
	return v, nil
}

// Set implements Store.
func (Keyring) Set(name, value string) error {
	if err := keyring.Set(service, name, value); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
	return nil
}

// Delete implements Store.
func (Keyring) Delete(name string) error {
	if err := keyring.Delete(service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("keyring: %w", err)
	}
	return nil
}

// Kind implements Store.
func (Keyring) Kind() string { return "keyring" }

// File keeps secrets in a JSON file readable only by the user. It is NOT
// encrypted: it is the fallback for machines without a keyring.
type File struct {
	Path string
	mu   sync.Mutex
}

func (f *File) read() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets file: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("secrets file %s is corrupt: %w", f.Path, err)
	}
	return m, nil
}

func (f *File) write(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return fmt.Errorf("secrets file: %w", err)
	}
	b, _ := json.Marshal(m)
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("secrets file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("secrets file: %w", err)
		}
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secrets file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secrets file: %w", err)
	}
	return os.Rename(tmp.Name(), f.Path)
}

// Get implements Store.
func (f *File) Get(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set implements Store.
func (f *File) Set(name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	m[name] = value
	return f.write(m)
}

// Delete implements Store.
func (f *File) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	delete(m, name)
	return f.write(m)
}

// Kind implements Store.
func (f *File) Kind() string { return "file" }

// Memory is an in-process store for tests.
type Memory struct {
	mu sync.Mutex
	M  map[string]string
}

// Get implements Store.
func (m *Memory) Get(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.M[name]; ok {
		return v, nil
	}
	return "", ErrNotFound
}

// Set implements Store.
func (m *Memory) Set(name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.M == nil {
		m.M = map[string]string{}
	}
	m.M[name] = value
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.M, name)
	return nil
}

// Kind implements Store.
func (m *Memory) Kind() string { return "memory" }

// Open returns the keyring if it works, otherwise the file fallback inside
// stateDir, calling warn once to say so.
func Open(stateDir string, warn func(string)) Store {
	probe := "probe-" + fmt.Sprint(os.Getpid())
	k := Keyring{}
	if err := k.Set(probe, "x"); err == nil {
		_ = k.Delete(probe)
		return k
	}
	if warn != nil {
		warn("no OS keyring available: the ingest token will be stored in " + filepath.Join(stateDir, "secrets.json") +
			" (readable only by you, but NOT encrypted)")
	}
	return &File{Path: filepath.Join(stateDir, "secrets.json")}
}
