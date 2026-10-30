// Package config holds modalfs's own accounts, saved mounts and settings.
//
// modalfs never follows the Modal CLI's "active profile": every mount is
// pinned to an account stored here (token id + secret), an environment and
// a volume ID, so switching profiles in the Modal CLI can't make a mount
// drift to another workspace. ~/.modal.toml is only read when the user asks
// to import from it.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/fsync"
)

type Account struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	TokenID      string `json:"token_id"`
	ServerURL    string `json:"server_url,omitempty"`
	Environment  string `json:"environment,omitempty"` // default env for new mounts
	Workspace    string `json:"workspace,omitempty"`
	Username     string `json:"username,omitempty"`
	ImportedFrom string `json:"imported_from,omitempty"` // modal profile name, informational only
	SecretStore  string `json:"secret_store"`            // keychain | file | env
	AddedAt      int64  `json:"added_at"`
}

// A Mount is pinned to its account. Logging in again adds a new account and
// never moves mounts to it, so recovering from a revoked token can mean
// disconnecting the old account and adding its volumes again. That's only
// acceptable while a mount is this cheap to set up again. Before a mount or
// an account holds anything costly to recreate, let a login replace an
// existing account's token or move its mounts (docs/design/browser-login.md).
type Mount struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Environment string `json:"environment,omitempty"`
	VolumeName  string `json:"volume_name"`
	VolumeID    string `json:"volume_id,omitempty"` // pinned on first mount
	Mountpoint  string `json:"mountpoint"`
	// FollowLabel: the mountpoint is a folder in the mount root named after
	// the volume's label, chosen again at each mount (daemon/folder.go).
	FollowLabel bool   `json:"follow_label,omitempty"`
	Mode        string `json:"mode,omitempty"` // auto | nfs | fuse
	ReadOnly    bool   `json:"read_only,omitempty"`
	AutoMount   bool   `json:"auto_mount,omitempty"`
	FilterJunk  bool   `json:"filter_junk"`             // keep .DS_Store / ._* local only
	WriteBackMS int64  `json:"write_back_ms,omitempty"` // 0 = default
	OnConflict  string `json:"on_conflict,omitempty"`   // keep-both | overwrite
	LastUsedAt  int64  `json:"last_used,omitempty"`     // when it was last unmounted (Unix seconds)
}

type Settings struct {
	CacheDir    string `json:"cache_dir,omitempty"`
	CacheSizeGB int    `json:"cache_size_gb,omitempty"`
	ReadAhead   int    `json:"read_ahead,omitempty"`
	Transport   string `json:"transport,omitempty"`  // "" (auto/grpc) | http1
	Language    string `json:"language,omitempty"`   // system | zh-Hant | en
	Appearance  string `json:"appearance,omitempty"` // system | light | dark
	MountRoot   string `json:"mount_root,omitempty"`
	// Remote downloads start Modal Functions, billed to the account, so
	// they're off until the user turns them on.
	RemoteDownloads      bool   `json:"remote_downloads,omitempty"`
	DownloadSize         string `json:"download_size,omitempty"`          // small (default), medium, large
	DownloadStallMinutes int    `json:"download_stall_minutes,omitempty"` // stop after this long without data (default 10)
	// ChartRange is the time a volume's speed chart shows, as last chosen
	// in the app: minute (default), tenMinutes or hour.
	ChartRange string `json:"chart_range,omitempty"`
}

type VolumeEncryption struct {
	VolumeID    string `json:"volume_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
	SecretStore string `json:"secret_store,omitempty"`
}

type File struct {
	Encryption []VolumeEncryption `json:"encryption,omitempty"`
	Accounts   []Account          `json:"accounts"`
	Mounts     []Mount            `json:"mounts"`
	Settings   Settings           `json:"settings"`
}

// Dir is the modalfs config directory (overridable with MODALFS_HOME).
func Dir() string {
	if d := os.Getenv("MODALFS_HOME"); d != "" {
		return d
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "modalfs")
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "modalfs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".modalfs")
}

func DefaultCacheDir() string {
	if d := os.Getenv("MODALFS_HOME"); d != "" {
		return filepath.Join(d, "cache")
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "modalfs")
	}
	return filepath.Join(Dir(), "cache")
}

func DefaultMountRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "ModalVolumes")
}

func (s Settings) WithDefaults() Settings {
	if s.CacheDir == "" {
		s.CacheDir = DefaultCacheDir()
	}
	if s.CacheSizeGB == 0 {
		s.CacheSizeGB = 20
	}
	if s.ReadAhead == 0 {
		s.ReadAhead = 16
	}
	if s.Language == "" {
		s.Language = "system"
	}
	if s.Appearance == "" {
		s.Appearance = "system"
	}
	if s.MountRoot == "" {
		s.MountRoot = DefaultMountRoot()
	}
	return s
}

// Store is a concurrency-safe handle on config.json.
type Store struct {
	mu   sync.Mutex
	path string
	f    File
}

func Open() (*Store, error) {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(Dir(), "config.json")}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(raw, &s.f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	raw, _ := json.MarshalIndent(s.f, "", "  ")
	return WriteAtomic(s.path, raw)
}

// WriteAtomic replaces path with raw (mode 0600) so that a crash leaves
// either the old or the new file, never an empty or partial one.
func WriteAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = fsync.File(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Persist the rename too; best effort, not every filesystem allows it.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = fsync.File(dir)
		_ = dir.Close()
	}
	return nil
}

// Snapshot returns a deep-enough copy of the current config.
func (s *Store) Snapshot() File {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := File{Settings: s.f.Settings.WithDefaults(), Encryption: append([]VolumeEncryption{}, s.f.Encryption...)}
	f.Accounts = append([]Account{}, s.f.Accounts...)
	f.Mounts = append([]Mount{}, s.f.Mounts...)
	return f
}

// Update applies fn to the config and persists it atomically: if it can't
// be saved, it isn't applied either.
func (s *Store) Update(fn func(f *File) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.f
	cp.Encryption = append([]VolumeEncryption{}, s.f.Encryption...)
	cp.Accounts = append([]Account{}, s.f.Accounts...)
	cp.Mounts = append([]Mount{}, s.f.Mounts...)
	if err := fn(&cp); err != nil {
		return err
	}
	old := s.f
	s.f = cp
	if err := s.saveLocked(); err != nil {
		s.f = old
		return err
	}
	return nil
}

func (s *Store) Account(id string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.f.Accounts {
		if a.ID == id || strings.EqualFold(a.Label, id) {
			return a, true
		}
	}
	return Account{}, false
}

func (s *Store) Mount(id string) (Mount, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.f.Mounts {
		if m.ID == id {
			return m, true
		}
	}
	return Mount{}, false
}

func NewID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func Now() int64 { return time.Now().Unix() }
