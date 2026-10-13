package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/fusefront"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/nfsfront"
	"github.com/t41372/modalfs/internal/vfs"
)

const legacyUsage = `modalfs run/serve: foreground mount without the daemon (for scripts and debugging)

Usage:
  modalfs run [flags] VOLUME MOUNTPOINT   mount in the foreground until Ctrl-C
  modalfs serve [flags] VOLUME            only run the NFS server and print the mount command

Flags:
`

func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for suf, m := range map[string]int64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40} {
		if strings.HasSuffix(s, suf) {
			mult = m
			s = strings.TrimSuffix(s, suf)
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	return int64(f * float64(mult)), err
}

func runLegacy(cmd string, argv []string) {
	fl := flag.NewFlagSet(cmd, flag.ExitOnError)
	profile := fl.String("profile", "", "Modal profile in ~/.modal.toml (default: active)")
	env := fl.String("env", "", "Modal environment")
	mode := fl.String("mode", "", "nfs or fuse (default: nfs on macOS, fuse on Linux)")
	addr := fl.String("addr", "127.0.0.1:0", "NFS listen address")
	cacheDir := fl.String("cache-dir", config.DefaultCacheDir()+"-run", "local block cache directory")
	cacheSize := fl.String("cache-size", "20G", "max size of the block cache")
	writeBack := fl.Duration("write-back", 3*time.Second, "idle time before modified files are committed")
	maxPending := fl.Duration("max-pending", 30*time.Second, "longest a file that keeps changing waits for its commit")
	keepJunk := fl.Bool("keep-junk", false, "upload macOS litter (.DS_Store, ._*) instead of keeping it in memory")
	dirTTL := fl.Duration("dir-cache-ttl", 10*time.Second, "how long directory listings are cached")
	readAhead := fl.Int("read-ahead", 8, "sequential read-ahead, in 8 MiB blocks")
	sidecarMin := fl.String("sidecar-min", "64M", "store block-hash sidecars (in /.modalfs) for files >= this size; 0 disables")
	onConflict := fl.String("on-conflict", "keep-both", "keep-both | overwrite, when a file changed remotely while edited locally")
	readOnly := fl.Bool("read-only", false, "mount read-only")
	create := fl.Bool("create", false, "create the volume (v2) if missing")
	keyStdin := fl.Bool("key-stdin", false, "read an encrypted volume key from terminal or stdin")
	verbose := fl.Bool("v", false, "verbose logging")
	debugFuse := fl.Bool("debug-fuse", false, "log every FUSE op")
	fl.Usage = func() { fmt.Fprint(os.Stderr, legacyUsage); fl.PrintDefaults() }
	_ = fl.Parse(argv)

	lvl := slog.LevelInfo
	if *verbose {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	cfg, err := modalapi.LoadConfig(*profile)
	if err != nil {
		fatal(err)
	}
	if *env != "" {
		cfg.Environment = *env
	}
	client, err := modalapi.New(cfg)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	args := fl.Args()
	if cmd == "run" {
		cmd = "mount"
	}
	if (cmd == "mount" && len(args) != 2) || (cmd == "serve" && len(args) != 1) {
		fl.Usage()
		os.Exit(2)
	}
	vol, err := client.LookupVolume(ctx, args[0], *create)
	if err != nil {
		fatal(fmt.Errorf("volume %q: %w", args[0], err))
	}
	cs, err := parseSize(*cacheSize)
	if err != nil {
		fatal(err)
	}
	sm, err := parseSize(*sidecarMin)
	if err != nil {
		fatal(err)
	}
	var cipher *encryption.Cipher
	if *keyStdin {
		key, err := encryption.ParseKey(readEncryptionKey())
		must(err)
		cipher = encryption.New(key)
	}
	v, err := vfs.New(client, vol, vfs.Options{
		Cipher:   cipher,
		CacheDir: *cacheDir, CacheMaxBytes: cs, DirCacheTTL: *dirTTL, WriteBack: *writeBack, MaxPending: *maxPending, FilterJunk: !*keepJunk,
		ReadAhead: *readAhead, SidecarMin: sm, OnConflict: *onConflict, ReadOnly: *readOnly, Logger: log,
	})
	if err != nil {
		fatal(err)
	}
	log.Info("volume ready", "name", vol.Name, "id", vol.ID, "version", vol.Version.String())

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	if *mode == "" {
		if runtime.GOOS == "darwin" || cmd == "serve" {
			*mode = "nfs"
		} else {
			*mode = "fuse"
		}
	}
	var mountpoint string
	if cmd == "mount" {
		mountpoint, _ = filepath.Abs(args[1])
		_ = os.MkdirAll(mountpoint, 0o755)
	}

	switch *mode {
	case "fuse":
		srv, err := fusefront.Mount(mountpoint, v, vol.Name, *readOnly, *debugFuse)
		if err != nil {
			fatal(err)
		}
		log.Info("mounted (FUSE)", "at", mountpoint)
		go func() {
			<-sig
			log.Info("unmounting; committing pending writes")
			if err := v.FlushAll(); err != nil {
				log.Error("flush", "err", err)
			}
			_ = srv.Unmount()
		}()
		srv.Wait()
	case "nfs":
		l, err := net.Listen("tcp", *addr)
		if err != nil {
			fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		go func() {
			if err := nfsfront.Serve(l, v, *readOnly); err != nil {
				log.Error("nfs server stopped", "err", err)
			}
		}()
		mcmd := mountCommand(port, mountpoint)
		if cmd == "serve" {
			log.Info("NFS server listening", "addr", l.Addr().String())
			fmt.Printf("mount with:\n  %s\n", mountCommand(port, "MOUNTPOINT"))
		} else {
			out, err := exec.Command("sh", "-c", mcmd).CombinedOutput()
			if err != nil {
				log.Warn("automatic mount failed; run it yourself (maybe with sudo)", "cmd", mcmd, "err", strings.TrimSpace(string(out)))
			} else {
				log.Info("mounted (NFS)", "at", mountpoint)
			}
		}
		<-sig
		log.Info("shutting down; committing pending writes")
		if mountpoint != "" {
			_ = exec.Command("umount", mountpoint).Run()
		}
		if err := v.Close(); err != nil {
			log.Error("flush", "err", err)
			os.Exit(1)
		}
	default:
		fatal(fmt.Errorf("unknown mode %q", *mode))
	}
	if err := v.Close(); err != nil {
		log.Error("flush", "err", err)
		os.Exit(1)
	}
}

func mountCommand(port int, mp string) string {
	if runtime.GOOS == "darwin" {
		// the daemon's options (internal/daemon/mounts.go)
		return fmt.Sprintf("mount -t nfs -o port=%d,mountport=%d,vers=3,tcp,locallocks,soft,intr,timeo=600,retrans=2,rsize=1048576,wsize=1048576,readahead=16,actimeo=1,noquota localhost:/ %q", port, port, mp)
	}
	return fmt.Sprintf("sudo mount -t nfs -o port=%d,mountport=%d,vers=3,tcp,nolock,rsize=1048576,wsize=1048576,readahead=16,actimeo=1 127.0.0.1:/ %q", port, port, mp)
}
