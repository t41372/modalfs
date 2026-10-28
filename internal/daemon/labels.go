package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/vfs"
	"github.com/t41372/modalfs/internal/volume"
)

// Volume labels: a name of the user's choosing for a volume, free of Modal's
// naming rules. It's kept in the volume itself, so every modalfs that uses
// the volume shows the same one; Modal's name stays the volume's identity
// (CLI arguments, pinning, search), and the label is only shown.
//
// The daemon reads the labels of the volumes it manages, no more often than
// directory listings are refreshed, and only when someone is looking (the
// app, the menu bar, the CLI ask for the state). It keeps what it read in
// labels.json, so a restart shows them before Modal answers. A label is
// written straight to Modal: the call returns once Modal has it.

const (
	labelPath  = vfs.MetaDir + "/volume.json"
	labelFresh = 10 * time.Second
	labelMax   = 80
)

// labelFile is .modalfs/volume.json in a volume.
type labelFile struct {
	V         int    `json:"v"`
	Label     string `json:"label"`
	UpdatedAt int64  `json:"updated_at"` // Unix milliseconds
}

type labelEntry struct {
	Label   string `json:"label,omitempty"`
	Checked int64  `json:"checked"` // Unix milliseconds of the last read from Modal
}

type labelStore struct {
	mu      sync.Mutex
	path    string
	byVol   map[string]labelEntry
	reading map[string]bool
	shown   map[string]string // the read error last shown for each volume
}

func newLabelStore(dir string) *labelStore {
	s := &labelStore{path: filepath.Join(dir, "labels.json"), byVol: map[string]labelEntry{}, reading: map[string]bool{}, shown: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.byVol)
	}
	return s
}

func (s *labelStore) get(volID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byVol[volID].Label
}

// lookup returns a volume's label and whether it has been read at all.
func (s *labelStore) lookup(volID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byVol[volID]
	return e.Label, ok
}

func (s *labelStore) set(volID, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byVol[volID] = labelEntry{Label: label, Checked: time.Now().UnixMilli()}
	s.saveLocked()
}

// failed records a failed read of a volume's label: the label read before
// is kept, and isn't asked for again before the next refresh. It reports
// whether to show the error. Only an encrypted volume's is shown, since a
// label there that can't be read may have been damaged or tampered with;
// not when Modal was out of reach (offline, or the Mac waking from sleep),
// which says nothing about the label; and once until a read succeeds.
func (s *labelStore) failed(volID string, err error, encrypted bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.byVol[volID]
	e.Checked = time.Now().UnixMilli()
	s.byVol[volID] = e
	if !encrypted || modalapi.IsUnreachable(err) || s.shown[volID] == err.Error() {
		return false
	}
	s.shown[volID] = err.Error()
	return true
}

func (s *labelStore) saveLocked() {
	b, err := json.Marshal(s.byVol)
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

// readLabel and writeLabel are the only code that touches a volume's
// label file, so a volume whose files are stored encrypted can decrypt and
// encrypt it here.

// readLabel reads an unlocked volume's label. Missing labels and unknown
// plaintext records use the Modal name; damaged encrypted labels are errors.
func readLabel(ctx context.Context, v *volume.Volume) (string, error) {
	data, err := v.ReadFile(ctx, labelPath, 64<<10)
	if modalapi.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var f labelFile
	if err := json.Unmarshal(data, &f); err != nil {
		if v.Cipher != nil {
			return "", fmt.Errorf("encrypted label has invalid JSON: %w", err)
		}
		return "", nil
	}
	if v.Cipher != nil && f.V != 1 {
		return "", errors.New("encrypted label has an unsupported format version")
	}
	return f.Label, nil
}

// writeLabel writes a volume's label to Modal; it returns once Modal has it.
func writeLabel(ctx context.Context, v *volume.Volume, label string) error {
	data, _ := json.Marshal(labelFile{V: 1, Label: label, UpdatedAt: time.Now().UnixMilli()})
	return v.WriteFile(ctx, labelPath, data)
}

// refreshLabels reads, in the background, the labels of managed volumes
// that weren't read in the last labelFresh.
func (d *Daemon) refreshLabels() {
	s := d.labels
	now := time.Now().UnixMilli()
	for _, m := range d.store.Snapshot().Mounts {
		if m.VolumeID == "" {
			continue
		}
		s.mu.Lock()
		e, busy := s.byVol[m.VolumeID], s.reading[m.VolumeID]
		due := !busy && now-e.Checked >= labelFresh.Milliseconds()
		if due {
			s.reading[m.VolumeID] = true
		}
		s.mu.Unlock()
		if due {
			go d.readLabelInto(m)
		}
	}
}

func (d *Daemon) readLabelInto(m config.Mount) {
	s := d.labels
	defer func() {
		s.mu.Lock()
		delete(s.reading, m.VolumeID)
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, _, err := d.clientFor(m.AccountID, m.Environment)
	if err == nil {
		var label string
		remote, encrypted, openErr := d.inspectVolume(ctx, c, m.VolumeID)
		err = openErr
		if err == nil && (!encrypted || remote.Cipher != nil) {
			label, err = readLabel(ctx, remote)
		}
		if err == nil {
			s.mu.Lock()
			changed := s.byVol[m.VolumeID].Label != label
			s.byVol[m.VolumeID] = labelEntry{Label: label, Checked: time.Now().UnixMilli()}
			delete(s.shown, m.VolumeID)
			if changed {
				s.saveLocked()
			}
			s.mu.Unlock()
			return
		}
	}
	d.log.Debug("couldn't read a volume's label", "volume", m.VolumeName, "err", err)
	encrypted, _, _ := d.encryptionFlags(m.VolumeID)
	if s.failed(m.VolumeID, err, encrypted) {
		d.emitC("error", "label_read_failed", m.ID, m.VolumeName, "could not read encrypted label: "+err.Error(), err.Error())
	}
}

// cleanLabel trims a label and checks it fits on a line.
func cleanLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > labelMax {
		return "", fmt.Errorf("a label can be at most %d characters", labelMax)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", errors.New("a label must be one line of text")
		}
	}
	return label, nil
}

// setLabel writes a volume's label to Modal ("" clears it).
func (d *Daemon) setLabel(ctx context.Context, accountID, env, volID, label string) (string, error) {
	label, err := cleanLabel(label)
	if err != nil {
		return "", err
	}
	for _, m := range d.store.Snapshot().Mounts {
		if m.VolumeID == volID && m.ReadOnly {
			return "", fmt.Errorf("%s is mounted read-only, so modalfs doesn't write to it, its label included; turn read-only off to change the label", m.VolumeName)
		}
	}
	c, _, err := d.clientFor(accountID, env)
	if err != nil {
		return "", err
	}
	remote, err := d.openVolume(ctx, c, volID)
	if err != nil {
		return "", err
	}
	if err := writeLabel(ctx, remote, label); err != nil {
		return "", friendly(err)
	}
	d.labels.set(volID, label)
	return label, nil
}
