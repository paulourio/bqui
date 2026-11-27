package app

import (
	"context"
	"sync"
	"sync/atomic"
)

// Fetch windows: results load a window of rows, then more on demand (when
// the grid scrolls near the end), instead of the whole result at once.
const (
	DefaultFetchWindow = 10_000
	PreviewWindow      = 1_000
)

// pager is a run's on-demand row fetching state.
type pager struct {
	want   atomic.Int64  // rows wanted so far
	demand chan struct{} // wakes a paused fetcher
	wg     sync.WaitGroup
}

func newPager(window int64) *pager {
	p := &pager{demand: make(chan struct{}, 1)}
	p.want.Store(window)

	return p
}

// FetchMore asks for n more rows than currently loaded (no-op when the
// results are complete or still loading the current window).
func (r *Run) FetchMore(n int64) {
	r.mu.Lock()
	more := r.snap.More
	r.mu.Unlock()

	if !more || r.pager == nil {
		return
	}

	rows := int64(0)
	if g := r.Snapshot().Grid; g != nil {
		rows, _, _ = g.Size()
	}

	for {
		cur := r.pager.want.Load()
		if cur >= rows+n || r.pager.want.CompareAndSwap(cur, rows+n) {
			break
		}
	}

	select {
	case r.pager.demand <- struct{}{}:
	default:
	}
}

// waitDemand pauses the fetcher until more than have rows are wanted. It
// returns false when the run is cancelled or closed.
func (r *Run) waitDemand(ctx context.Context, have int64) bool {
	if r.pager.want.Load() > have {
		return true
	}

	r.update(func(s *RunSnapshot) { s.More, s.LoadingMore = true, false })

	for r.pager.want.Load() <= have {
		select {
		case <-ctx.Done():
			return false
		case <-r.pager.demand:
		}
	}

	r.update(func(s *RunSnapshot) { s.LoadingMore = true })

	return true
}

// fetchInBackground runs fetch on its own goroutine and returns when the
// first window is loaded (ready called), the fetch ends, or it fails before
// the window. Later errors are recorded on the snapshot (FetchErr).
func (r *Run) fetchInBackground(fetch func(ready func()) error) error {
	first := make(chan error, 1)

	var once sync.Once

	ready := func() { once.Do(func() { first <- nil }) }

	r.pager.wg.Go(func() {
		err := fetch(ready)

		r.update(func(s *RunSnapshot) {
			s.More, s.LoadingMore = false, false
			if err != nil {
				s.FetchErr = err
			}
		})

		once.Do(func() { first <- err })
	})

	return <-first
}
