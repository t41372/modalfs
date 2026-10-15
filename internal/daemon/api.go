package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/downloads"
	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/vfs"
	"github.com/t41372/modalfs/internal/volume"
	"github.com/t41372/modalfs/internal/webui"
)

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

var volNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func (d *Daemon) routes() http.Handler {
	mux := http.NewServeMux()
	d.encryptionRoutes(mux)
	mux.Handle("GET /", webui.Handler())

	// ping takes no locks, so a daemon busy with a long commit still answers it.
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": Version})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		d.refreshLabels() // someone is looking: keep the labels they see current
		writeJSON(w, 200, d.State())
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		writeJSON(w, 200, d.Events(after))
	})
	// now=1 quits without waiting for uploads, also when quitting already began.
	mux.HandleFunc("POST /api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
		d.Shutdown(r.URL.Query().Get("now") == "1")
	})
	// A one-time link to the browser UI. The TCP port refuses this (see
	// tcpGuard), so only clients of the Unix socket can mint one.
	mux.HandleFunc("POST /api/ui/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"url": d.uiURL + "#token=" + d.auth.mint()})
	})

	// ----- accounts -----
	mux.HandleFunc("POST /api/accounts", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Label, TokenID, TokenSecret, Environment, ServerURL string
			SecretFromEnv                                       bool
		}
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		a, err := d.AddAccount(r.Context(), in.Label, in.TokenID, in.TokenSecret, in.ServerURL, in.Environment, "", in.SecretFromEnv)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, a)
	})
	mux.HandleFunc("GET /api/import/profiles", func(w http.ResponseWriter, r *http.Request) {
		ps, err := modalapi.ReadProfiles()
		if err != nil {
			writeJSON(w, 200, []any{})
			return
		}
		have := map[string]bool{}
		for _, a := range d.store.Snapshot().Accounts {
			have[a.TokenID] = true
		}
		type pv struct {
			Name     string `json:"name"`
			TokenID  string `json:"token_id"`
			Active   bool   `json:"active"`
			Imported bool   `json:"imported"`
		}
		out := []pv{}
		for _, p := range ps {
			out = append(out, pv{p.Name, p.TokenID, p.Active, have[p.TokenID]})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/import/profiles", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Names []string }
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		added, errs := d.ImportProfiles(r.Context(), in.Names)
		writeJSON(w, 200, map[string]any{"added": added, "errors": errs})
	})
	mux.HandleFunc("PATCH /api/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Label       *string
			Environment *string
		}
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		id := r.PathValue("id")
		err := d.store.Update(func(f *config.File) error {
			for i := range f.Accounts {
				if f.Accounts[i].ID == id {
					if in.Label != nil && strings.TrimSpace(*in.Label) != "" {
						f.Accounts[i].Label = strings.TrimSpace(*in.Label)
					}
					if in.Environment != nil {
						f.Accounts[i].Environment = *in.Environment
					}
					return nil
				}
			}
			return errors.New("account not found")
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		d.dropClients(id)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /api/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := d.RemoveAccount(r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/accounts/{id}/verify", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := d.clientFor(r.PathValue("id"), "")
		if err != nil {
			writeErr(w, err)
			return
		}
		id, err := c.WhoAmI(r.Context())
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		writeJSON(w, 200, id)
	})
	// Replaces an account's token, for one that expired or was revoked. The new
	// token must belong to the same workspace, so no mount moves elsewhere.
	mux.HandleFunc("POST /api/accounts/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ TokenID, TokenSecret string }
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		a, err := d.ReplaceToken(r.Context(), r.PathValue("id"), in.TokenID, in.TokenSecret)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, a)
	})
	// The account owner's profile picture, as a data URL ("" if none).
	mux.HandleFunc("GET /api/accounts/{id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := d.store.Account(r.PathValue("id")); !ok {
			writeJSON(w, 404, map[string]string{"error": "no such account"})
			return
		}
		writeJSON(w, 200, map[string]string{"data_url": d.avatarDataURL(r.Context(), r.PathValue("id"))})
	})
	mux.HandleFunc("GET /api/accounts/{id}/environments", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := d.clientFor(r.PathValue("id"), "")
		if err != nil {
			writeErr(w, err)
			return
		}
		envs, err := c.ListEnvironments(r.Context())
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		writeJSON(w, 200, envs)
	})

	// ----- volumes -----
	type volView struct {
		modalapi.VolumeInfo
		Format        string `json:"format"`
		Mounted       bool   `json:"mounted"`
		MountID       string `json:"mount_id,omitempty"`
		Label         string `json:"label,omitempty"` // only when known: labels aren't read for every volume
		Encrypted     bool   `json:"encrypted,omitempty"`
		Locked        bool   `json:"locked,omitempty"`
		KeyRemembered bool   `json:"key_remembered,omitempty"`
	}
	mux.HandleFunc("GET /api/accounts/{id}/volumes", func(w http.ResponseWriter, r *http.Request) {
		acct := r.PathValue("id")
		env := r.URL.Query().Get("env")
		c, _, err := d.clientFor(acct, env)
		if err != nil {
			writeErr(w, err)
			return
		}
		vs, err := c.ListVolumes(r.Context())
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		st := d.State()
		out := make([]volView, len(vs))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i, v := range vs {
			if !v.V2() {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				vv := &out[i]
				vv.Encrypted, vv.Locked, vv.KeyRemembered = d.listedEncryption(r.Context(), c, v.ID)
			}()
		}
		wg.Wait()
		for i, v := range vs {
			vv := &out[i]
			vv.VolumeInfo, vv.Format = v, strings.ToLower(strings.TrimPrefix(v.Version.String(), "VOLUME_FS_VERSION_"))
			if !vv.Locked {
				vv.Label = d.labels.get(v.ID)
			}
			if vv.Format == "unspecified" {
				vv.Format = "v1"
			}
			for _, m := range st.Mounts {
				if m.AccountID == acct && (m.VolumeID == v.ID || (m.VolumeID == "" && m.VolumeName == v.Name)) {
					vv.MountID = m.ID
					vv.Mounted = m.State == StateMounted
				}
			}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name, Environment, Key, Label string
			Encrypted                     bool
			Remember                      *bool
		}
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		if !volNameRe.MatchString(in.Name) {
			writeErr(w, errors.New("volume names use letters, digits, '.', '_' and '-' (max 64)"))
			return
		}
		c, _, err := d.clientFor(r.PathValue("id"), in.Environment)
		if err != nil {
			writeErr(w, err)
			return
		}
		// Checked before the volume exists, so a bad label creates nothing.
		if _, err := cleanLabel(in.Label); err != nil {
			writeErr(w, err)
			return
		}
		var key encryption.Key
		if in.Encrypted {
			key, err = encryption.ParseKey(in.Key)
			if err != nil {
				writeErr(w, err)
				return
			}
		}
		v, err := c.CreateVolume(r.Context(), in.Name)
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		if in.Encrypted {
			d.encryption.mu.Lock()
			err = d.rememberKey(v.ID, key, in.Remember == nil || *in.Remember)
			d.encryption.mu.Unlock()
			remote := &volume.Volume{Client: c, ID: v.ID, Cipher: encryption.New(key)}
			if err == nil {
				err = remote.WriteFile(r.Context(), ".modalfs_keep", nil)
			}
			if err == nil {
				err = remote.WriteMarker(r.Context())
			}
			if err != nil {
				writeErr(w, fmt.Errorf("created volume %s (%s), but encryption initialization failed: %w; keep your key and retry recovery or delete this empty volume", v.Name, v.ID, err))
				return
			}
		}
		if strings.TrimSpace(in.Label) != "" {
			if _, err := d.setLabel(r.Context(), r.PathValue("id"), in.Environment, v.ID, in.Label); err != nil {
				writeErr(w, fmt.Errorf("created volume %s, but couldn't save its label: %w; set it again from the volume's page", v.Name, err))
				return
			}
		}
		d.emitC("info", "vol_created", "", v.Name, "created volume "+v.Name)
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes/{vol}/rename", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Environment string }
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		if !volNameRe.MatchString(in.Name) {
			writeErr(w, errors.New("invalid volume name"))
			return
		}
		c, _, err := d.clientFor(r.PathValue("id"), in.Environment)
		if err != nil {
			writeErr(w, err)
			return
		}
		volID := r.PathValue("vol")
		if err := c.RenameVolume(r.Context(), volID, in.Name); err != nil {
			writeErr(w, friendly(err))
			return
		}
		_ = d.store.Update(func(f *config.File) error {
			for i := range f.Mounts {
				if f.Mounts[i].VolumeID == volID {
					f.Mounts[i].VolumeName = in.Name
				}
			}
			return nil
		})
		d.emitC("info", "vol_renamed", "", in.Name, "renamed volume to "+in.Name)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// A volume's label: GET reads it from the volume now; POST sets it ("" clears).
	mux.HandleFunc("GET /api/accounts/{id}/volumes/{vol}/label", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := d.clientFor(r.PathValue("id"), r.URL.Query().Get("env"))
		if err != nil {
			writeErr(w, err)
			return
		}
		volID := r.PathValue("vol")
		remote, encrypted, err := d.inspectVolume(r.Context(), c, volID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if encrypted && remote.Cipher == nil {
			writeJSON(w, 200, map[string]string{"label": ""})
			return
		}
		label, err := readLabel(r.Context(), remote)
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		d.labels.set(volID, label)
		writeJSON(w, 200, map[string]string{"label": label})
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes/{vol}/label", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Label, Environment string }
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		label, err := d.setLabel(r.Context(), r.PathValue("id"), in.Environment, r.PathValue("vol"), in.Label)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"label": label})
	})
	mux.HandleFunc("POST /api/accounts/{id}/volumes/{vol}/delete", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Confirm, Name, Environment string }
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		if in.Confirm == "" || in.Confirm != in.Name {
			writeErr(w, errors.New("type the volume name to confirm deletion"))
			return
		}
		volID := r.PathValue("vol")
		for _, m := range d.State().Mounts {
			if m.VolumeID == volID && (m.State == StateMounted || m.State == StateStarting || m.State == StateStopping) {
				writeErr(w, errors.New("unmount this volume before deleting it"))
				return
			}
		}
		c, _, err := d.clientFor(r.PathValue("id"), in.Environment)
		if err != nil {
			writeErr(w, err)
			return
		}
		// make sure the ID still has the name the user typed
		vs, err := c.ListVolumes(r.Context())
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		found := false
		for _, v := range vs {
			if v.ID == volID && v.Name == in.Name {
				found = true
			}
		}
		if !found {
			writeErr(w, errors.New("volume not found under that name"))
			return
		}
		if err := c.DeleteVolume(r.Context(), volID); err != nil {
			writeErr(w, friendly(err))
			return
		}
		_ = d.store.Update(func(f *config.File) error {
			keep := f.Mounts[:0]
			for _, m := range f.Mounts {
				if m.VolumeID != volID {
					keep = append(keep, m)
				}
			}
			f.Mounts = keep
			return nil
		})
		d.emitC("info", "vol_deleted", "", in.Name, "deleted volume "+in.Name)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	// ----- mounts -----
	mux.HandleFunc("POST /api/mounts", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			config.Mount
			Start bool `json:"start"`
			// wait answers once mounted or failed; otherwise it answers at
			// once, with the mount starting in the background.
			Wait bool `json:"wait"`
		}
		in.FilterJunk = true
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		m, err := d.AddMount(in.Mount)
		if err != nil {
			writeErr(w, err)
			return
		}
		if in.Start {
			start := d.StartMountAsync
			if in.Wait {
				start = d.StartMount
			}
			if err := start(m.ID); err != nil {
				writeJSON(w, 200, map[string]any{"mount": m, "error": err.Error()})
				return
			}
		}
		writeJSON(w, 200, map[string]any{"mount": m})
	})
	mux.HandleFunc("PATCH /api/mounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var in map[string]any
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		err := d.store.Update(func(f *config.File) error {
			// StartMount reads the config after marking the mount starting,
			// so this check and the edit below can't miss a mount going live.
			d.mu.Lock()
			live := d.liveLocked(id)
			d.mu.Unlock()
			for i := range f.Mounts {
				m := &f.Mounts[i]
				if m.ID != id {
					continue
				}
				old := *m
				for k, v := range in {
					switch k {
					case "auto_mount":
						m.AutoMount, _ = v.(bool)
					case "read_only":
						m.ReadOnly, _ = v.(bool)
					case "filter_junk":
						m.FilterJunk, _ = v.(bool)
					case "mode":
						m.Mode, _ = v.(string)
					case "on_conflict":
						m.OnConflict, _ = v.(string)
						if err := checkOnConflict(m.OnConflict); err != nil {
							return err
						}
					case "mountpoint":
						s, _ := v.(string)
						if s != "" {
							mp, err := checkMountpoint(s, f, id, false)
							if err != nil {
								return err
							}
							if mp != m.Mountpoint {
								m.Mountpoint, m.FollowLabel = mp, false // the user's choice from now on
							}
						}
					case "write_back_ms":
						f, _ := v.(float64)
						m.WriteBackMS = int64(f)
					case "volume_id": // "" re-pins on next mount
						m.VolumeID, _ = v.(string)
					}
				}
				if live != "" && mountTimeChanged(old, *m) {
					switch live {
					case StateStarting:
						return fmt.Errorf("%s is still mounting; wait for it to finish, then unmount it to change this", m.VolumeName)
					case StateStopping:
						return fmt.Errorf("%s is still unmounting; wait for it to finish, then try again", m.VolumeName)
					}
					return fmt.Errorf("%s is mounted; unmount it first to change this", m.VolumeName)
				}
				return nil
			}
			return errors.New("mount not found")
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /api/mounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := d.StopMount(id, false); err != nil {
			writeErr(w, err)
			return
		}
		err := d.store.Update(func(f *config.File) error {
			// It may have been started again since StopMount returned.
			keep := f.Mounts[:0]
			for _, m := range f.Mounts {
				if m.ID != id {
					keep = append(keep, m)
				} else if err := d.busyErr(m); err != nil {
					return err
				}
			}
			f.Mounts = keep
			return nil
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		d.traffic.drop(id)
		d.conflicts.clear(id, "")
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/mounts/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		if err := d.StartMount(r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// force=1 unmounts now, even while apps use it; patient=1 (the window's
	// quit checklist) retries a busy unmount for a few seconds before failing.
	mux.HandleFunc("POST /api/mounts/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		force := r.URL.Query().Get("force") == "1"
		patient := r.URL.Query().Get("patient") == "1"
		if err := d.stopMount(r.PathValue("id"), stopOpts{now: force, force: force, patient: patient}); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/mounts/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := d.SyncMount(r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	// Shows the mounted folder, or a file in it (path, relative to the mount).
	mux.HandleFunc("POST /api/mounts/{id}/reveal", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Path string }
		_ = readJSON(r, &in)
		m, ok := d.store.Mount(r.PathValue("id"))
		if !ok {
			writeErr(w, errors.New("mount not found"))
			return
		}
		// What's mounted is asked of the mount itself: this process serves
		// it, and file I/O on it from here can deadlock (see AGENTS.md).
		var fsys *vfs.FS
		var mp string
		d.mu.Lock()
		if rm := d.running[m.ID]; rm != nil && rm.state == StateMounted {
			fsys, mp = rm.fs, rm.mountpoint
		}
		d.mu.Unlock()
		if fsys == nil {
			writeErr(w, fmt.Errorf("%s isn't mounted; mount it first", m.VolumeName))
			return
		}
		if err := revealIn(fsys, mp, in.Path); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	// ----- settings / cache / service -----
	mux.HandleFunc("PATCH /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			config.Settings
			// A pointer, so false turns it off and leaving it out changes nothing.
			RemoteDownloads *bool `json:"remote_downloads"`
		}
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		if in.CacheSizeGB < 0 {
			writeErr(w, errors.New("cache size must be at least 1 GB"))
			return
		}
		if _, ok := downloads.Sizes[in.DownloadSize]; in.DownloadSize != "" && !ok {
			writeErr(w, fmt.Errorf("unknown download size %q (use small, medium or large)", in.DownloadSize))
			return
		}
		if in.DownloadStallMinutes < 0 {
			writeErr(w, errors.New("the stall limit must be at least 1 minute"))
			return
		}
		if t := in.Transport; t != "" && t != "auto" && t != "http1" {
			writeErr(w, fmt.Errorf("unknown transport %q (use auto or http1)", t))
			return
		}
		if r := in.ChartRange; r != "" && r != "minute" && r != "tenMinutes" && r != "hour" {
			writeErr(w, fmt.Errorf("unknown chart range %q (use minute, tenMinutes or hour)", r))
			return
		}
		oldTransport := d.store.Snapshot().Settings.Transport
		err := d.store.Update(func(f *config.File) error {
			s := &f.Settings
			if in.CacheSizeGB > 0 {
				s.CacheSizeGB = in.CacheSizeGB
			}
			if in.Language != "" {
				s.Language = in.Language
			}
			if in.Appearance != "" {
				s.Appearance = in.Appearance
			}
			if in.ReadAhead > 0 {
				s.ReadAhead = in.ReadAhead
			}
			if in.MountRoot != "" {
				root, err := checkMountpoint(in.MountRoot, f, "", true)
				if err != nil {
					return err
				}
				s.MountRoot = root
			}
			if in.Transport != "" {
				if in.Transport == "auto" {
					s.Transport = ""
				} else {
					s.Transport = in.Transport
				}
			}
			if in.RemoteDownloads != nil {
				s.RemoteDownloads = *in.RemoteDownloads
			}
			if in.DownloadSize != "" {
				s.DownloadSize = in.DownloadSize
			}
			if in.DownloadStallMinutes > 0 {
				s.DownloadStallMinutes = in.DownloadStallMinutes
			}
			if in.ChartRange != "" {
				s.ChartRange = in.ChartRange
			}
			return nil
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		d.cache.SetMax(int64(d.store.Snapshot().Settings.CacheSizeGB) << 30)
		if d.store.Snapshot().Settings.Transport != oldTransport {
			d.dropClients("") // mounted volumes keep theirs until they're unmounted
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/cache/clear", func(w http.ResponseWriter, r *http.Request) {
		d.cache.Clear()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/service/{action}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.PathValue("action") {
		case "install":
			err = InstallService()
		case "uninstall":
			err = UninstallService()
		default:
			err = errors.New("unknown action")
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, serviceStatus())
	})
	mux.HandleFunc("POST /api/open", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Target string }
		_ = readJSON(r, &in)
		switch in.Target {
		case "logs":
			_ = openPath(LogPath())
		case "config":
			_ = openPath(config.Dir())
		case "modal-dashboard":
			_ = openPath("https://modal.com/storage")
		case "modal-tokens":
			_ = openPath("https://modal.com/settings/tokens")
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	d.downloadRoutes(mux)
	d.insightRoutes(mux)
	return mux
}

// ---------- operations shared by API and CLI ----------

func (d *Daemon) AddAccount(ctx context.Context, label, tokenID, secret, server, env, importedFrom string, secretFromEnv bool) (config.Account, error) {
	tokenID, secret = strings.TrimSpace(tokenID), strings.TrimSpace(secret)
	if tokenID == "" || (secret == "" && !secretFromEnv) {
		return config.Account{}, errors.New("token ID and token secret are required")
	}
	// Fail early before asking Modal; the check that counts is in Update below.
	for _, a := range d.store.Snapshot().Accounts {
		if a.TokenID == tokenID {
			return a, fmt.Errorf("this token is already added as %q", a.Label)
		}
	}
	if server == "" {
		server = "https://api.modal.com"
	}
	if secretFromEnv {
		secret = os.Getenv("MODAL_TOKEN_SECRET")
	}
	c, err := modalapi.New(modalapi.Config{TokenID: tokenID, TokenSecret: secret, ServerURL: server, Transport: d.transport()})
	if err != nil {
		return config.Account{}, err
	}
	defer closeClient(c)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	who, err := c.WhoAmI(cctx)
	if err != nil {
		return config.Account{}, fmt.Errorf("Modal rejected this token: %w", err)
	}
	a := config.Account{ID: config.NewID("acct"), Label: strings.TrimSpace(label), TokenID: tokenID, ServerURL: server,
		Environment: env, Workspace: who.WorkspaceName, Username: who.Username, ImportedFrom: importedFrom, AddedAt: config.Now()}
	if a.Label == "" {
		a.Label = who.WorkspaceName
	}
	if secretFromEnv {
		a.SecretStore = "env"
	} else {
		store, err := config.SaveSecret(a.ID, secret)
		if err != nil {
			return config.Account{}, err
		}
		a.SecretStore = store
	}
	err = d.store.Update(func(f *config.File) error {
		for _, x := range f.Accounts {
			if x.TokenID == tokenID {
				return fmt.Errorf("this token is already added as %q", x.Label)
			}
		}
		f.Accounts = append(f.Accounts, a)
		return nil
	})
	if err != nil {
		if derr := config.DeleteSecret(a); derr != nil {
			d.log.Warn("could not delete the secret of an account that wasn't added", "account", a.ID, "err", derr)
		}
		return config.Account{}, err
	}
	d.emitC("info", "acct_added", "", "", "added account "+a.Label+" ("+a.Workspace+")", a.Label, a.Workspace)
	return a, nil
}

// ReplaceToken gives an account a new token from the same workspace. The
// secret is checked with Modal first; mounts already mounted keep the old
// token until they're mounted again.
func (d *Daemon) ReplaceToken(ctx context.Context, id, tokenID, secret string) (config.Account, error) {
	tokenID, secret = strings.TrimSpace(tokenID), strings.TrimSpace(secret)
	a, ok := d.store.Account(id)
	if !ok {
		return a, errors.New("account not found")
	}
	if tokenID == "" || secret == "" {
		return a, errors.New("token ID and token secret are required")
	}
	for _, x := range d.store.Snapshot().Accounts {
		if x.TokenID == tokenID && x.ID != id {
			return a, fmt.Errorf("this token is already added as %q", x.Label)
		}
	}
	c, err := modalapi.New(modalapi.Config{TokenID: tokenID, TokenSecret: secret, ServerURL: a.ServerURL, Transport: d.transport()})
	if err != nil {
		return a, err
	}
	defer closeClient(c)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	who, err := c.WhoAmI(cctx)
	if err != nil {
		return a, fmt.Errorf("Modal rejected this token: %w", err)
	}
	if a.Workspace != "" && who.WorkspaceName != a.Workspace {
		return a, fmt.Errorf("this token belongs to workspace %q, not %q; add it as another account instead", who.WorkspaceName, a.Workspace)
	}
	old := a
	if old.SecretStore == "env" {
		// The new secret is stored; the environment variable is no longer read.
		old.SecretStore = ""
	}
	store, err := config.SaveSecret(a.ID, secret)
	if err != nil {
		return a, err
	}
	err = d.store.Update(func(f *config.File) error {
		for i := range f.Accounts {
			if f.Accounts[i].ID == id {
				f.Accounts[i].TokenID, f.Accounts[i].SecretStore = tokenID, store
				f.Accounts[i].Workspace, f.Accounts[i].Username = who.WorkspaceName, who.Username
				a = f.Accounts[i]
				return nil
			}
		}
		return errors.New("account not found")
	})
	if err != nil {
		return a, err
	}
	// A keychain secret was overwritten in place; one moved between stores leaves the old copy behind.
	if old.SecretStore != "" && old.SecretStore != store {
		_ = config.DeleteSecret(old)
	}
	d.dropClients(id)
	d.emitC("info", "acct_token", "", "", "replaced the token of "+a.Label, a.Label)
	return a, nil
}

func (d *Daemon) ImportProfiles(ctx context.Context, names []string) ([]config.Account, []string) {
	ps, err := modalapi.ReadProfiles()
	if err != nil {
		return nil, []string{err.Error()}
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var added []config.Account
	var errs []string
	for _, p := range ps {
		if len(want) > 0 && !want[p.Name] {
			continue
		}
		a, err := d.AddAccount(ctx, p.Name, p.TokenID, p.TokenSecret, p.ServerURL, p.Environment, p.Name, false)
		if err != nil {
			errs = append(errs, p.Name+": "+err.Error())
			continue
		}
		added = append(added, a)
	}
	return added, errs
}

func (d *Daemon) RemoveAccount(id string) error {
	a, ok := d.store.Account(id)
	if !ok {
		return errors.New("account not found")
	}
	if d.dl != nil {
		for _, j := range d.dl.store.List() {
			if j.AccountID == a.ID && !downloads.Ended(j.State) {
				return fmt.Errorf("a remote download of this account is %s; pause or cancel it first", j.State)
			}
		}
	}
	err := d.store.Update(func(f *config.File) error {
		for _, m := range f.Mounts {
			if m.AccountID != a.ID {
				continue
			}
			if err := d.busyErr(m); err != nil {
				return fmt.Errorf("%w (it belongs to this account)", err)
			}
		}
		keepA := f.Accounts[:0]
		for _, x := range f.Accounts {
			if x.ID != a.ID {
				keepA = append(keepA, x)
			}
		}
		f.Accounts = keepA
		keepM := f.Mounts[:0]
		for _, m := range f.Mounts {
			if m.AccountID != a.ID {
				keepM = append(keepM, m)
			}
		}
		f.Mounts = keepM
		return nil
	})
	if err != nil {
		return err
	}
	d.dropClients(a.ID)
	if d.avatars != nil {
		d.avatars.forget(a.ID)
	}
	if err := config.DeleteSecret(a); err != nil {
		return fmt.Errorf("removed the account, but could not delete its token secret: %w", err)
	}
	return nil
}

func (d *Daemon) AddMount(m config.Mount) (config.Mount, error) {
	a, ok := d.store.Account(m.AccountID)
	if !ok {
		return m, errors.New("choose an account")
	}
	if !volNameRe.MatchString(m.VolumeName) {
		return m, errors.New("choose a volume")
	}
	if err := checkOnConflict(m.OnConflict); err != nil {
		return m, err
	}
	if m.Mode == "" {
		m.Mode = "auto"
	}
	m.ID = config.NewID("mnt")
	if m.Environment == "" {
		m.Environment = a.Environment // or, for the workspace's default, pinned when it mounts
	}
	var dup config.Mount
	err := d.store.Update(func(f *config.File) error {
		for _, x := range f.Mounts {
			// a mount not pinned yet may be for any environment
			if x.AccountID == m.AccountID && x.VolumeName == m.VolumeName && (x.Environment == m.Environment || x.Environment == "" || m.Environment == "") {
				dup = x
				return errors.New("this volume already has a saved mount")
			}
		}
		mp, err := checkMountpoint(m.Mountpoint, f, "", false)
		if err != nil {
			return err
		}
		if mp == "" {
			root := expandHome(f.Settings.WithDefaults().MountRoot)
			mp = filepath.Join(root, m.VolumeName)
			for _, x := range f.Mounts {
				if filepath.Clean(expandHome(x.Mountpoint)) == mp {
					mp = filepath.Join(root, m.VolumeName+"-"+sanitize(a.Label))
				}
			}
			if mp, err = checkMountpoint(mp, f, "", false); err != nil {
				return err
			}
			m.FollowLabel = true
		}
		m.Mountpoint = mp
		f.Mounts = append(f.Mounts, m)
		return nil
	})
	if dup.ID != "" {
		return dup, err
	}
	return m, err
}

// checkMountpoint cleans a mountpoint from the API and refuses one that
// would hide or nest inside another saved mount (other than self) or
// modalfs's own files. "" stays "", meaning the default. With root it checks
// Settings' mount root instead, which may hold mounts but not sit inside one.
// checkOnConflict refuses a conflict policy modalfs doesn't have ("" is
// the default, keep-both).
func checkOnConflict(p string) error {
	if p != "" && p != "keep-both" && p != "overwrite" {
		return fmt.Errorf("unknown on_conflict %q (use keep-both or overwrite)", p)
	}
	return nil
}

func checkMountpoint(p string, f *config.File, self string, root bool) (string, error) {
	if p == "" {
		return "", nil
	}
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not a full path; use one that starts with / or ~/", p)
	}
	p = filepath.Clean(p)
	if p == "/" {
		return "", errors.New("/ can't be used; choose a folder")
	}
	own := []struct{ name, dir string }{
		{"modalfs's settings folder", config.Dir()},
		{"modalfs's cache folder", f.Settings.WithDefaults().CacheDir},
	}
	for _, o := range own {
		dir, err := filepath.Abs(o.dir)
		if err != nil {
			return "", err
		}
		if p == dir || inside(p, dir) || (!root && inside(dir, p)) {
			return "", fmt.Errorf("%s overlaps %s (%s); choose another folder", p, o.name, dir)
		}
	}
	for _, x := range f.Mounts {
		if x.ID == self || x.Mountpoint == "" {
			continue
		}
		mp := filepath.Clean(expandHome(x.Mountpoint))
		switch {
		case p == mp && !root:
			return "", fmt.Errorf("%s is already the mountpoint of %s; choose another folder", p, x.VolumeName)
		case p == mp || inside(p, mp):
			return "", fmt.Errorf("%s is inside the mountpoint of %s (%s); choose a folder outside it", p, x.VolumeName, mp)
		case !root && inside(mp, p):
			return "", fmt.Errorf("%s contains the mountpoint of %s (%s); choose a folder that doesn't", p, x.VolumeName, mp)
		}
	}
	return p, nil
}

// inside reports whether path p is strictly below dir. Both are clean.
func inside(p, dir string) bool {
	return dir == "/" || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// liveLocked returns the live state of a mount, or "" when it isn't
// running. The caller holds d.mu.
func (d *Daemon) liveLocked(id string) string {
	if r := d.running[id]; r != nil && r.state != StateError {
		return r.state
	}
	return ""
}

// busyErr says why a saved mount can't be removed right now, or nil.
func (d *Daemon) busyErr(m config.Mount) error {
	d.mu.Lock()
	live := d.liveLocked(m.ID)
	d.mu.Unlock()
	switch live {
	case StateMounted:
		return fmt.Errorf("%s is mounted; unmount it first", m.VolumeName)
	case StateStarting:
		return fmt.Errorf("%s is still mounting; wait for it to finish mounting, unmount it, then try again", m.VolumeName)
	case StateStopping:
		return fmt.Errorf("%s is still unmounting; wait for it to finish, then try again", m.VolumeName)
	}
	return nil
}

// mountTimeChanged reports whether an edit touches a setting that only
// takes effect when the volume is mounted.
func mountTimeChanged(old, m config.Mount) bool {
	or := func(s, def string) string {
		if s == "" {
			return def
		}
		return s
	}
	return filepath.Clean(expandHome(old.Mountpoint)) != filepath.Clean(expandHome(m.Mountpoint)) ||
		old.ReadOnly != m.ReadOnly || or(old.Mode, "auto") != or(m.Mode, "auto") ||
		old.FilterJunk != m.FilterJunk || or(old.OnConflict, "keep-both") != or(m.OnConflict, "keep-both") ||
		old.WriteBackMS != m.WriteBackMS || old.VolumeID != m.VolumeID
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == ':' {
			return '-'
		}
		return r
	}, s)
}

func openPath(p string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, p)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it
	return nil
}
