package modalapi

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Block transfers to and from signed URLs. On a slow link one 8 MiB block
// can take minutes, more so while others share the link, so a transfer is
// given up on only once no bytes have moved for stallTimeout, never after a
// fixed time. Uploads run a few at a time, as many as keep each one short
// on the link they're on (see upSlots).

// An upload's last few MiB sit in the socket's send buffer after modalfs has
// handed them over, so once the body is all sent it gets answerTimeout to
// drain them and hear back.
const (
	stallTimeout  = 60 * time.Second
	answerTimeout = 5 * time.Minute
)

var errStalled = errors.New("the transfer stopped making progress")

// watch cancels a transfer attempt that stops making progress.
type watch struct{ t *time.Timer }

func (w watch) moved(int) { w.t.Reset(stallTimeout) }
func (w watch) sent()     { w.t.Reset(answerTimeout) }

// transfer runs f with retries like withRetry, but each attempt runs until
// it stops making progress (see watch) rather than for a fixed time.
func transfer[T any](ctx context.Context, f func(ctx context.Context, w watch) (T, error)) (T, error) {
	var zero T
	delay := 200 * time.Millisecond
	for attempt := 0; ; attempt++ {
		actx, cancel := context.WithCancelCause(ctx)
		stall := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
		v, err := f(actx, watch{stall})
		stall.Stop()
		if err != nil && context.Cause(actx) == errStalled && ctx.Err() == nil {
			err = status.Error(codes.Unavailable, errStalled.Error())
		}
		cancel(nil)
		if err == nil {
			return v, nil
		}
		if attempt >= 5 || !retryable(err) || ctx.Err() != nil {
			return zero, err
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// progressReader reports every read to moved, and the end to done.
type progressReader struct {
	r     io.Reader
	moved func(int)
	done  func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.moved(n)
	}
	if err == io.EOF && p.done != nil {
		p.done()
	}
	return n, err
}

// Each block upload has a fixed cost on Modal's side (about a second and a
// half before it answers), so one connection moves only a few MiB/s and a
// fast link needs many uploads at once. On a slow link the same number
// would only make each block take longer, and blocks waiting longer to be
// committed (see vfs/ahead.go). upSlots sizes the uploads in flight from the
// speed they reach together: enough that each block takes about
// blockTarget, within [minUploads, maxUploads].
const (
	minUploads   = 4
	firstUploads = 8 // until the speed is known
	maxUploads   = 32
	blockTarget  = 5 * time.Second
	rateWindow   = 2 * time.Second
)

type upSlots struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int
	used  int

	// the speed over the current window, measured only while uploads
	// never stopped (an idle moment would make the link look slow)
	winStart time.Time
	winBytes int64
	idle     bool
	rate     float64 // bytes a second, over the last window with uploads all along
}

func newUpSlots() *upSlots {
	s := &upSlots{limit: firstUploads}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// acquire waits for a free slot.
func (s *upSlots) acquire(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.used >= s.limit {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.cond.Wait()
	}
	if s.used == 0 && (s.idle || s.winStart.IsZero()) {
		s.winStart, s.winBytes, s.idle = time.Now(), 0, false
	}
	s.used++
	return nil
}

func (s *upSlots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used--
	if s.used == 0 {
		s.idle = true
	}
	s.cond.Broadcast()
}

// moved counts bytes sent, and resizes the slots once a window is over.
func (s *upSlots) moved(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.winBytes += int64(n)
	el := time.Since(s.winStart)
	if el < rateWindow {
		return
	}
	if !s.idle {
		rate := float64(s.winBytes) / el.Seconds()
		s.rate = rate
		s.limit = min(max(int(rate*blockTarget.Seconds()/BlockSize), minUploads), maxUploads)
		s.cond.Broadcast()
	}
	s.winStart, s.winBytes, s.idle = time.Now(), 0, s.used == 0
}

func (s *upSlots) Limit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

func (s *upSlots) Rate() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rate
}
