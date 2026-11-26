//go:build darwin

package objc

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Main-thread dispatch uses exactly one purego callback (drainTrampoline)
// registered for the whole process. Go closures are queued on the Go side and
// drained in FIFO order on the AppKit main thread.
var (
	mainQueue      uintptr
	dispatchAsyncF func(queue, ctx, fn uintptr)
	drainTramp     uintptr

	pendingMu sync.Mutex
	pending   []func()
)

func initDispatch() error {
	lib, err := purego.Dlopen(libSystemPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("objc: dlopen libSystem: %w", err)
	}

	// dispatch_get_main_queue() is a macro for &_dispatch_main_q.
	mainQueue, err = purego.Dlsym(lib, "_dispatch_main_q")
	if err != nil {
		return fmt.Errorf("objc: dlsym _dispatch_main_q: %w", err)
	}

	purego.RegisterLibFunc(&dispatchAsyncF, lib, "dispatch_async_f")
	drainTramp = purego.NewCallback(drainMain)

	return nil
}

// Main schedules fn to run on the AppKit main thread. It is safe to call from
// any goroutine, including the main thread itself (fn then runs on the next
// run-loop turn).
func Main(fn func()) {
	pendingMu.Lock()
	pending = append(pending, fn)
	schedule := len(pending) == 1
	pendingMu.Unlock()

	if schedule {
		dispatchAsyncF(mainQueue, 0, drainTramp)
	}
}

func drainMain(_ uintptr) {
	pendingMu.Lock()
	fns := pending
	pending = nil
	pendingMu.Unlock()

	for _, fn := range fns {
		WithPool(fn)
	}
}
