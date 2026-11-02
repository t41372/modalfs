package vfs

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestCollisionReportsAgainAfterHealthyLookup(t *testing.T) {
	for _, absent := range []bool{false, true} {
		label := "unique"
		if absent {
			label = "absent"
		}
		t.Run(label, func(t *testing.T) {
			const raw, alias = "caf\u00e9", "cafe\u0301"
			var reports atomic.Int64
			f := newFakeFS(t, Options{NameKey: testNameKey, DirCacheTTL: time.Nanosecond, OnEvent: func(kind, _, _ string) {
				if kind == "unreadable" {
					reports.Add(1)
				}
			}})
			f.put(t, raw, []byte("first"))
			f.put(t, alias, []byte("second"))
			collision := func(want int64) {
				t.Helper()
				if _, _, err := f.Names().Lookup("", alias); !errors.Is(err, syscall.ENOTSUP) {
					t.Fatalf("collision lookup: %v", err)
				}
				if got := reports.Load(); got != want {
					t.Fatalf("collision reports: got %d, want %d", got, want)
				}
			}
			collision(1)
			collision(1)
			if err := f.client.Remove(context.Background(), f.vol.ID, alias, false); err != nil {
				t.Fatal(err)
			}
			if absent {
				if err := f.client.Remove(context.Background(), f.vol.ID, raw, false); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := f.Names().Lookup("", alias)
			if absent && !errors.Is(err, os.ErrNotExist) || !absent && err != nil {
				t.Fatalf("healthy lookup: %v", err)
			}
			f.put(t, raw, []byte("first"))
			f.put(t, alias, []byte("second"))
			collision(2)
			collision(2)
		})
	}
}

func TestHealthyLookupRetainsUnaddressableNameNotice(t *testing.T) {
	var reports atomic.Int64
	f := newFakeFS(t, Options{NameKey: testNameKey, DirCacheTTL: time.Nanosecond, OnEvent: func(kind, _, _ string) {
		if kind == "unreadable" {
			reports.Add(1)
		}
	}})
	// Both stored spellings fit 255 bytes, but their shared client spelling
	// exceeds that limit. Repairing the collision does not repair its length.
	raw := strings.Repeat("\u00e9", 127)
	alias := strings.Repeat("\u00e9", 126) + "e\u0301"
	f.put(t, raw, []byte("first"))
	f.put(t, alias, []byte("second"))
	if _, err := f.Names().ReadDirLimit("", 255); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Names().Lookup("", raw); !errors.Is(err, syscall.ENOTSUP) {
		t.Fatalf("collision lookup: %v", err)
	}
	if err := f.client.Remove(context.Background(), f.vol.ID, alias, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Names().Lookup("", raw); err != nil {
		t.Fatalf("unique raw lookup: %v", err)
	}
	if entries, err := f.Names().ReadDirLimit("", 255); err != nil || len(entries) != 0 {
		t.Fatalf("long name unexpectedly addressable: %+v, %v", entries, err)
	}
	if got := reports.Load(); got != 1 {
		t.Fatalf("unchanged length problem reported %d times, want 1", got)
	}
}
