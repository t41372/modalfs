package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/volume"
)

// errLocked is a start or action on an encrypted volume whose key this
// computer doesn't have: the volume isn't broken, it waits for Unlock.
var errLocked = errors.New("volume is encrypted and locked; unlock it first")

type encryptionState struct {
	mu   sync.Mutex
	keys map[string]*encryption.Cipher
}

func (d *Daemon) encryptionRecord(id string) config.VolumeEncryption {
	for _, v := range d.store.Snapshot().Encryption {
		if v.VolumeID == id {
			return v
		}
	}
	return config.VolumeEncryption{VolumeID: id}
}

func (d *Daemon) saveEncryption(v config.VolumeEncryption) error {
	return d.store.Update(func(f *config.File) error {
		for i := range f.Encryption {
			if f.Encryption[i].VolumeID == v.VolumeID {
				f.Encryption[i] = v
				return nil
			}
		}
		f.Encryption = append(f.Encryption, v)
		return nil
	})
}

// cipherLocked loads only the recorded secret store, never a forgotten key
// left somewhere else. The encryption mutex also serializes locking.
func (d *Daemon) cipherLocked(id string) (*encryption.Cipher, error) {
	if c := d.encryption.keys[id]; c != nil {
		return c, nil
	}
	record := d.encryptionRecord(id)
	if record.SecretStore == "" {
		return nil, nil
	}
	text, err := config.LoadVolumeKey(record)
	if err != nil {
		return nil, fmt.Errorf("cannot read remembered volume key: %w", err)
	}
	key, err := encryption.ParseKey(text)
	if err != nil {
		return nil, err
	}
	c := encryption.New(key)
	if record.Fingerprint != key.Fingerprint() {
		return nil, errors.New("remembered key doesn't match this volume; unlock with the original key")
	}
	if d.encryption.keys == nil {
		d.encryption.keys = map[string]*encryption.Cipher{}
	}
	d.encryption.keys[id] = c
	return c, nil
}

// inspectVolume discovers encryption from Modal, including when the optional
// marker was removed. Local records keep a known volume locked even if all
// of its remote data has been removed.
func (d *Daemon) inspectVolume(ctx context.Context, c *modalapi.Client, id string) (*volume.Volume, bool, error) {
	encrypted, marker, err := volume.Detect(ctx, c, id)
	if err != nil {
		return nil, encrypted, err
	}
	d.encryption.mu.Lock()
	defer d.encryption.mu.Unlock()
	record := d.encryptionRecord(id)
	known := false
	for _, r := range d.store.Snapshot().Encryption {
		if r.VolumeID == id {
			known = true
		}
	}
	encrypted = encrypted || known
	if encrypted && !known {
		if marker != nil {
			record.Fingerprint = marker.Fingerprint
		}
		if err := d.saveEncryption(record); err != nil {
			return nil, true, err
		}
	}
	key, err := d.cipherLocked(id)
	if err != nil {
		return nil, encrypted, err
	}
	return &volume.Volume{Client: c, ID: id, Cipher: key}, encrypted, nil
}

func (d *Daemon) openVolume(ctx context.Context, c *modalapi.Client, id string) (*volume.Volume, error) {
	v, encrypted, err := d.inspectVolume(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if encrypted && v.Cipher == nil {
		return nil, errLocked
	}
	return v, nil
}

func (d *Daemon) rememberKey(id string, key encryption.Key, remember bool) error {
	record := d.encryptionRecord(id)
	if record.Fingerprint != "" && record.Fingerprint != key.Fingerprint() {
		return errors.New("this isn't the right key for this volume")
	}
	record.Fingerprint = key.Fingerprint()
	var err error
	if remember {
		record.SecretStore, err = config.SaveVolumeKey(record, key.Text())
	} else if record.SecretStore != "" {
		err = config.DeleteVolumeKey(record)
		record.SecretStore = ""
	}
	if err != nil {
		return err
	}
	if err := d.saveEncryption(record); err != nil {
		return err
	}
	if d.encryption.keys == nil {
		d.encryption.keys = map[string]*encryption.Cipher{}
	}
	d.encryption.keys[id] = encryption.New(key)
	return nil
}

func (d *Daemon) unlockVolume(ctx context.Context, c *modalapi.Client, id, text string, remember bool) error {
	key, err := encryption.ParseKey(text)
	if err != nil {
		return err
	}
	encrypted, marker, err := volume.Detect(ctx, c, id)
	if err != nil {
		return err
	}
	if !encrypted {
		return errors.New("volume is not encrypted; converting existing volumes is not supported")
	}
	v := &volume.Volume{Client: c, ID: id, Cipher: encryption.New(key)}
	if err := v.VerifyKey(ctx, marker); err != nil {
		return err
	}
	d.encryption.mu.Lock()
	err = d.rememberKey(id, key, remember)
	d.encryption.mu.Unlock()
	if err != nil {
		return err
	}
	if marker == nil {
		if err := v.WriteMarker(ctx); err != nil {
			return fmt.Errorf("key accepted, but restoring the encryption marker failed: %w", err)
		}
	}
	return nil
}

func (d *Daemon) lockVolume(account, id string) error {
	d.encryption.mu.Lock()
	defer d.encryption.mu.Unlock()
	f := d.store.Snapshot()
	known := false
	for _, record := range f.Encryption {
		if record.VolumeID == id {
			known = true
		}
	}
	if !known {
		return errors.New("volume is not known as encrypted; list volumes or unlock it first")
	}
	d.mu.Lock()
	for _, m := range f.Mounts {
		if m.VolumeID != id && !(m.VolumeID == "" && m.AccountID == account) {
			continue
		}
		if r := d.running[m.ID]; r != nil && (r.state == StateMounted || r.state == StateStarting || r.state == StateStopping || r.closing) {
			d.mu.Unlock()
			return errors.New("unmount this volume and wait for pending uploads before locking it")
		}
	}
	d.mu.Unlock()
	record := d.encryptionRecord(id)
	if record.SecretStore != "" {
		if err := config.DeleteVolumeKey(record); err != nil {
			return fmt.Errorf("couldn't forget the remembered key; volume remains unlocked: %w", err)
		}
		record.SecretStore = ""
		if err := d.saveEncryption(record); err != nil {
			return err
		}
	}
	delete(d.encryption.keys, id)
	if d.labels != nil {
		d.labels.set(id, "")
	}
	if d.usage != nil {
		d.usage.cancel(id)
	}
	return nil
}

// encryptionFlags says what this computer knows about a volume's encryption:
// whether it's encrypted, locked (no key here), and whether the key is
// remembered rather than kept until modalfs quits. It never reads the secret
// store, which the state the UI polls would otherwise do every time; a
// remembered key that can't be read fails the mount, which says why.
func (d *Daemon) encryptionFlags(id string) (encrypted, locked, remembered bool) {
	d.encryption.mu.Lock()
	defer d.encryption.mu.Unlock()
	for _, record := range d.store.Snapshot().Encryption {
		if record.VolumeID == id {
			remembered = record.SecretStore != ""
			return true, !remembered && d.encryption.keys[id] == nil, remembered
		}
	}
	return false, false, false
}

// listedEncryption is what the volume list shows about one volume: what this
// computer knows, or else whether Modal has the volume's marker. Recognising
// an encrypted volume whose marker was deleted takes listing its files, so
// that waits until it's mounted or unlocked.
func (d *Daemon) listedEncryption(ctx context.Context, c *modalapi.Client, id string) (encrypted, locked, remembered bool) {
	if encrypted, locked, remembered = d.encryptionFlags(id); encrypted {
		return encrypted, locked, remembered
	}
	_, err := c.ReadFile(ctx, id, volume.MarkerPath, 4096)
	if err != nil && !modalapi.IsNotFound(err) {
		d.log.Debug("couldn't check a volume for an encryption marker", "volume", id, "err", err)
	}
	return err == nil, err == nil, false
}

func (d *Daemon) refuseEncryptedDownload(ctx context.Context, account, env, id string) error {
	c, _, err := d.clientFor(account, env)
	if err != nil {
		return err
	}
	_, encrypted, err := d.inspectVolume(ctx, c, id)
	if err != nil {
		return err
	}
	if encrypted {
		return errors.New("remote downloads cannot write into encrypted volumes: the download agent never receives the encryption key")
	}
	return nil
}

func (d *Daemon) encryptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/accounts/{id}/volumes/lookup", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := d.clientFor(r.PathValue("id"), r.URL.Query().Get("env"))
		if err != nil {
			writeErr(w, err)
			return
		}
		v, err := c.LookupVolume(r.Context(), r.URL.Query().Get("name"), false)
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		writeJSON(w, 200, v)
	})

	mux.HandleFunc("POST /api/encryption/key", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"key": encryption.NewKey().Text()})
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes/{vol}/unlock", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key, Environment string
			Remember         bool
		}
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		c, _, err := d.clientFor(r.PathValue("id"), in.Environment)
		if err == nil {
			err = d.unlockVolume(r.Context(), c, r.PathValue("vol"), in.Key, in.Remember)
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"encrypted": true, "locked": false})
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes/{vol}/lock", func(w http.ResponseWriter, r *http.Request) {
		if err := d.lockVolume(r.PathValue("id"), r.PathValue("vol")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/accounts/{id}/volumes/{vol}/key", func(w http.ResponseWriter, r *http.Request) {
		d.encryption.mu.Lock()
		defer d.encryption.mu.Unlock()
		id := r.PathValue("vol")
		if d.encryptionRecord(id).SecretStore == "" {
			writeErr(w, errors.New("the key isn't remembered on this computer"))
			return
		}
		c, err := d.cipherLocked(id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"key": c.Key().Text()})
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes/{vol}/decrypt", d.decryptVolume)
}
