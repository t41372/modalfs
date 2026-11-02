//go:build darwin

package filename

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestAllFoundationScalars(t *testing.T) {
	key, err := Key()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	data, err := exec.CommandContext(ctx, "swift", "-e", foundationScalarCorpus).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("generate Foundation scalar corpus: %v\n%s", err, exit.Stderr)
		}
		t.Fatalf("generate Foundation scalar corpus: %v", err)
	}
	var changed map[string][]rune
	if err = json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	for r := rune(1); r <= 0x10ffff; r++ {
		if r == '/' || r == '.' || r >= 0xd800 && r <= 0xdfff {
			continue
		}
		want := string(r)
		if v, ok := changed[strconv.Itoa(int(r))]; ok {
			want = string(v)
		}
		got, err := key(string(r))
		if err != nil || got != want {
			t.Fatalf("U+%04X: got %U, want %U, err %v", r, []rune(got), []rune(want), err)
		}
	}
}

func TestNativeNames(t *testing.T) {
	key, err := Key()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"\ufeff":                           "\ufeff",
		"\ufeffcafé":                       "\ufeffcafe\u0301",
		"café":                             "cafe\u0301",
		"a" + strings.Repeat("\u0301", 31): "a" + strings.Repeat("\u0301", 31),
		"a" + strings.Repeat("\u0301", 30) + "\u034f\u0301": "a" + strings.Repeat("\u0301", 30) + "\u034f\u0301",
		"a" + strings.Repeat("\u0315\u0300", 20):            "a" + strings.Repeat("\u0300", 20) + strings.Repeat("\u0315", 20),
		"é\u20d0\u0323":                                     "e\u0323\u0301\u20d0",
		"KÅ\uf900\U0002f800":                                "KÅ\uf900\U0002f800",
		"café/가":                                            "cafe\u0301/\u1100\u1161",
		"":                                                  "",
	}
	for input, want := range cases {
		if got, err := key(input); err != nil || got != want {
			t.Errorf("%q: %U want %U, %v", input, []rune(got), []rune(want), err)
		}
	}
	for _, bad := range []string{"a\x00b", "\xff", "a\xc3"} {
		if got, err := key(bad); !errors.Is(err, syscall.EINVAL) || got != "" {
			t.Errorf("invalid input silently converted: %q -> %q %v", bad, got, err)
		}
	}
}

func TestNativeConcurrentGC(t *testing.T) {
	key, err := Key()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				got, err := key("café/â\u0084¢.rar")
				if err != nil || got != "cafe\u0301/a\u0302\u0084¢.rar" {
					t.Errorf("native concurrent conversion: %q %v", got, err)
					return
				}
				if j%100 == 0 {
					runtime.GC()
				}
			}
		}()
	}
	wg.Wait()
}

func TestConversionFailureDoesNotFallBack(t *testing.T) {
	for _, stage := range []string{"create", "size", "convert", "terminator", "empty"} {
		t.Run(stage, func(t *testing.T) {
			released := 0
			cf := &coreFoundation{
				create:      func(uintptr, *uint16, int64) uintptr { return 1 },
				maximumSize: func(uintptr) int64 { return 8 },
				filesystemBytes: func(_ uintptr, out *byte, size int64) uint8 {
					if stage == "convert" {
						return 0
					}
					if stage == "terminator" {
						for i := range unsafe.Slice(out, int(size)) {
							unsafe.Slice(out, int(size))[i] = 'x'
						}
					}
					return 1
				},
				release: func(uintptr) { released++ },
			}
			if stage == "create" {
				cf.create = func(uintptr, *uint16, int64) uintptr { return 0 }
			}
			if stage == "size" {
				cf.maximumSize = func(uintptr) int64 { return 0 }
			}
			if got, err := cf.key("é"); got != "" || !errors.Is(err, syscall.EIO) {
				t.Fatalf("failed native %s: %q %v", stage, got, err)
			}
			wantRelease := 1
			if stage == "create" {
				wantRelease = 0
			}
			if released != wantRelease {
				t.Fatalf("released native strings: %d, want %d", released, wantRelease)
			}
		})
	}
}

func BenchmarkNativeKey(b *testing.B) {
	key, err := Key()
	if err != nil {
		b.Fatal(err)
	}
	for _, s := range []string{"readme.txt", "café/â\u0084¢.rar"} {
		b.Run(s, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := key(s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Byte slicing matters here: Swift String offsets count grapheme clusters,
// so slicing a prefix ending in '/' would drop a leading combining character.
const foundationScalarCorpus = `
import Foundation
var changed: [String: [UInt32]] = [:]
for value in UInt32(1)...UInt32(0x10FFFF) {
    guard value != 0x2F, value != 0x2E, let scalar = Unicode.Scalar(value) else { continue }
    let original = String(scalar)
    let converted = URL(fileURLWithPath: "/tmp/" + original, isDirectory: false)
        .withUnsafeFileSystemRepresentation { pointer in pointer.map { String(cString: $0) } }
    guard let converted else { fatalError("Foundation rejected a valid scalar") }
    let bytes = Array(converted.utf8)
    guard Array(bytes.prefix(5)) == Array("/tmp/".utf8) else { fatalError("unexpected path prefix") }
    let suffix = String(decoding: bytes.dropFirst(5), as: UTF8.self)
    if Array(suffix.utf8) != Array(original.utf8) {
        changed[String(value)] = suffix.unicodeScalars.map(\.value)
    }
}
let data = try JSONSerialization.data(withJSONObject: changed)
FileHandle.standardOutput.write(data)
`
