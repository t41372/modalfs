package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/fsync"
)

// Account pictures: the profile picture of the user each token belongs to,
// from Modal's member list. The daemon fetches and keeps the image, and the
// UI gets it as a data URL from the API, so pages never load anything from
// another site. A picture is refreshed once a day; a failed fetch keeps the
// old one, and an account without one is asked again a few hours later.

const (
	avatarFresh   = 24 * time.Hour
	avatarRetry   = 6 * time.Hour
	avatarMaxSize = 512 << 10
)

type avatar struct {
	URL     string `json:"url,omitempty"`
	Type    string `json:"type,omitempty"`
	Data    []byte `json:"data,omitempty"`
	Fetched int64  `json:"fetched"`
}

type avatarStore struct {
	mu       sync.Mutex
	path     string
	byAcct   map[string]avatar
	fetching map[string]bool
	http     *http.Client
}

func newAvatarStore(dir string) *avatarStore {
	s := &avatarStore{path: filepath.Join(dir, "avatars.json"), byAcct: map[string]avatar{}, fetching: map[string]bool{},
		http: &http.Client{Timeout: 15 * time.Second}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.byAcct)
	}
	return s
}

// avatarDataURL returns the account's picture as a data URL, or "" if it
// has none. The first time, it waits for the fetch; after that a stale
// picture is shown while a new one is fetched in the background.
func (d *Daemon) avatarDataURL(ctx context.Context, accountID string) string {
	s := d.avatars
	s.mu.Lock()
	a, ok := s.byAcct[accountID]
	age := time.Since(time.Unix(a.Fetched, 0))
	stale := ok && ((len(a.Data) > 0 && age > avatarFresh) || (len(a.Data) == 0 && age > avatarRetry))
	busy := s.fetching[accountID]
	if (!ok || stale) && !busy {
		s.fetching[accountID] = true
	}
	s.mu.Unlock()
	switch {
	case !ok && !busy:
		d.fetchAvatar(ctx, accountID)
		s.mu.Lock()
		a = s.byAcct[accountID]
		s.mu.Unlock()
	case stale && !busy:
		go d.fetchAvatar(context.Background(), accountID)
	}
	if len(a.Data) == 0 {
		return ""
	}
	return "data:" + a.Type + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
}

func (d *Daemon) fetchAvatar(ctx context.Context, accountID string) {
	s := d.avatars
	defer func() {
		s.mu.Lock()
		delete(s.fetching, accountID)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, _, err := d.clientFor(accountID, "")
	if err != nil {
		return
	}
	u, err := c.AvatarURL(ctx)
	if err != nil {
		return // keep what we have; ask again next time
	}
	next := avatar{URL: u, Fetched: time.Now().Unix()}
	if u != "" {
		typ, data, err := s.download(ctx, u)
		if err != nil {
			d.log.Info("account picture not fetched", "account", accountID, "err", err)
			s.mu.Lock()
			if old, ok := s.byAcct[accountID]; ok && len(old.Data) > 0 {
				next = old // keep the old picture until a fetch works
				next.Fetched = time.Now().Add(-avatarFresh + avatarRetry).Unix()
			}
			s.mu.Unlock()
		} else {
			next.Type, next.Data = typ, data
		}
	}
	s.mu.Lock()
	s.byAcct[accountID] = next
	s.saveLocked()
	s.mu.Unlock()
}

// download fetches an image over HTTPS, at most avatarMaxSize bytes.
func (s *avatarStore) download(ctx context.Context, raw string) (string, []byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return "", nil, fmt.Errorf("not an https URL")
	}
	if u.Host == "avatars.githubusercontent.com" {
		q := u.Query()
		q.Set("s", "96") // GitHub serves the size asked for; the UI shows 20 pt
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, err
	}
	r, err := s.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	typ, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(typ, "image/") || typ == "image/svg+xml" {
		return "", nil, fmt.Errorf("not an image (%s)", typ)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, avatarMaxSize+1))
	if err != nil {
		return "", nil, err
	}
	if len(data) > avatarMaxSize {
		return "", nil, fmt.Errorf("image larger than %d KiB", avatarMaxSize>>10)
	}
	return typ, data, nil
}

// forget drops a removed account's picture.
func (s *avatarStore) forget(accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byAcct[accountID]; ok {
		delete(s.byAcct, accountID)
		s.saveLocked()
	}
}

func (s *avatarStore) saveLocked() {
	b, err := json.Marshal(s.byAcct)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, err = f.Write(b)
	if err == nil {
		err = fsync.File(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = os.Rename(tmp, s.path)
	}
}
