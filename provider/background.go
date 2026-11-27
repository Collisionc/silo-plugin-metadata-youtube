package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrStillWorking says a listing is still being fetched from YouTube when the
// caller's time ran out. The work goes on and its result is cached, so asking
// again shortly gets the answer without spending the quota a second time.
var ErrStillWorking = errors.New("youtube: still fetching from YouTube; ask again shortly")

const (
	// backgroundTimeout bounds one piece of background work. Listing the
	// largest channel the client lists (25,000 videos, 500 pages) takes a
	// few minutes.
	backgroundTimeout = 15 * time.Minute
	// answerMargin is how long before a caller's deadline it is answered,
	// so that ErrStillWorking reaches Silo and its log rather than Silo's
	// own deadline error.
	answerMargin = 2 * time.Second
)

// background runs work that can take longer than Silo waits for one call
// (30 seconds): a large channel's listing, or the details of thousands of
// videos. Each piece of work runs once per key, on a context of its own, so a
// caller that stops waiting does not cancel it, and callers asking for the
// same key while it runs wait for the same result.
type background struct {
	timeout time.Duration
	margin  time.Duration

	mu      sync.Mutex
	running map[string]*job
}

type job struct {
	done  chan struct{}
	value any
	err   error
}

func newBackground() *background {
	return &background{timeout: backgroundTimeout, margin: answerMargin, running: make(map[string]*job)}
}

// do runs work under key, or joins the run already under way, and waits for
// its result until shortly before ctx's deadline. If the time runs out first
// it returns ErrStillWorking and the work carries on; work stores what it
// fetches, so the next caller finds it.
func (b *background) do(ctx context.Context, key string, work func(context.Context) (any, error)) (any, error) {
	b.mu.Lock()
	j, ok := b.running[key]
	if !ok {
		j = &job{done: make(chan struct{})}
		b.running[key] = j
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), b.timeout)
		go func() {
			defer cancel()
			j.value, j.err = work(workCtx)
			b.mu.Lock()
			delete(b.running, key)
			b.mu.Unlock()
			close(j.done)
		}()
	}
	b.mu.Unlock()

	var giveUp <-chan time.Time
	if deadline, ok := ctx.Deadline(); ok {
		timer := time.NewTimer(time.Until(deadline) - b.margin)
		defer timer.Stop()
		giveUp = timer.C
	}
	select {
	case <-j.done:
		return j.value, j.err
	case <-giveUp:
		return nil, ErrStillWorking
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrStillWorking
		}
		return nil, ctx.Err()
	}
}
