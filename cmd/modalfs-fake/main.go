// modalfs-fake runs the fake Modal in internal/modalfake, for development and
// manual testing without a Modal account. It prints the environment variables
// that point modalfs at it:
//
//	go run ./cmd/modalfs-fake -up 20M -latency 50ms
//	eval "$(...)"; modalfs run --create test ~/tmp/mnt
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/t41372/modalfs/internal/modalfake"
)

func main() {
	dir := flag.String("dir", filepath.Join(os.TempDir(), "modalfs-fake"), "where blocks are stored")
	latency := flag.Duration("latency", 0, "added to every call and block transfer")
	up := flag.String("up", "0", "upload bandwidth per second, like 20M (0 = unlimited)")
	down := flag.String("down", "0", "download bandwidth per second (0 = unlimited)")
	trim := flag.Bool("trim-zeros", false, "omit trailing zero bytes from block downloads, as Modal may")
	flag.Parse()

	s, err := modalfake.Start(*dir, modalfake.Options{Latency: *latency, UpBytesPerSec: size(*up), DownBytesPerSec: size(*down), TrimZeros: *trim})
	if err != nil {
		fmt.Fprintln(os.Stderr, "modalfs-fake:", err)
		os.Exit(1)
	}
	fmt.Printf("export MODAL_SERVER_URL=%s MODAL_TOKEN_ID=ak-fake MODAL_TOKEN_SECRET=as-fake MODAL_CONFIG_PATH=/dev/null\n", s.URL)
	fmt.Fprintf(os.Stderr, "fake Modal at %s, blocks in %s (Ctrl-C to stop)\n", s.URL, *dir)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	s.Close()
}

func size(s string) int64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for suf, m := range map[string]int64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30} {
		if v, ok := strings.CutSuffix(s, suf); ok {
			s, mult = v, m
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f * float64(mult))
}
