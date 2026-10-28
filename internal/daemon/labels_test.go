package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A label that can't be read is shown once, only for an encrypted volume,
// and not when Modal was out of reach: a Mac waking from sleep is offline
// for a moment and reads every label then.
func TestLabelReadFailedShown(t *testing.T) {
	s := newLabelStore(t.TempDir())
	offline := []error{
		fmt.Errorf(".modalfs/volume.json block 0: %w", status.Error(codes.Unavailable, `Get "https://example.com/block": context deadline exceeded`)),
		status.Error(codes.Unavailable, "name resolver error: produced zero addresses"),
		status.Error(codes.DeadlineExceeded, "context deadline exceeded while waiting for connections to become ready"),
		fmt.Errorf("reading: %w", context.DeadlineExceeded),
	}
	for _, err := range offline {
		if s.failed("vo-1", err, true) {
			t.Errorf("shown while offline: %v", err)
		}
	}
	if s.byVol["vo-1"].Checked == 0 {
		t.Error("a failed read isn't recorded, so it's tried again at once")
	}

	damaged := errors.New("encryption authentication failed: damaged or unsupported block header")
	if s.failed("vo-2", damaged, false) {
		t.Error("shown for a volume that isn't encrypted")
	}
	if !s.failed("vo-1", damaged, true) {
		t.Error("a damaged encrypted label isn't shown")
	}
	if s.failed("vo-1", damaged, true) {
		t.Error("the same error is shown again on the next refresh")
	}
	if !s.failed("vo-1", errors.New("encrypted label has an unsupported format version"), true) {
		t.Error("a different error isn't shown")
	}
	delete(s.shown, "vo-1") // as a read that succeeds does
	if !s.failed("vo-1", damaged, true) {
		t.Error("an error isn't shown again after a read succeeded")
	}
}
