package downloads

import (
	"context"
	"fmt"
	"testing"

	modal "github.com/modal-labs/modal-client/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Only a call's own outcome says it ended. A poll that failed (offline,
// say) doesn't: the Function may well still be running, and billed.
func TestCallResultEndsOnlyOnTheCallsOutcome(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		ended bool
	}{
		{"offline", fmt.Errorf("FunctionGetOutputs failed: %w", status.Error(codes.Unavailable, "offline")), false},
		{"poll deadline", context.DeadlineExceeded, false},
		{"still running", modal.FunctionTimeoutError{Exception: "Timeout exceeded: 0.0s"}, false},
		{"its own time limit", modal.FunctionTimeoutError{Exception: "Function exceeded its timeout"}, true},
		{"failed or cancelled", modal.RemoteError{Exception: "download failed"}, true},
		{"its container failed", modal.InternalFailure{Exception: "container terminated"}, true},
	} {
		if _, ended, _ := callResult(nil, c.err); ended != c.ended {
			t.Errorf("%s: ended=%v, want %v", c.name, ended, c.ended)
		}
	}
}
