package main

import (
	"testing"

	"github.com/t41372/modalfs/internal/downloads"
)

// A gap in the history (an interruption) shows as blank columns.
func TestSparkline(t *testing.T) {
	h := []downloads.Sample{{0, 0, 10}, {1, 10, 10}, {2, 20, 5}, {8, 30, 0}, {9, 40, 10}, {10, 50, 10}}
	got := sparkline(h, 10)
	if want := "█▅  ▁█"; got != want {
		t.Fatalf("sparkline = %q, want %q", got, want)
	}
}
