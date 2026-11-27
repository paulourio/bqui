//go:build darwin

package ui

import (
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/paulourio/bqui/internal/objc"
)

const (
	stallPing      = 500 * time.Millisecond
	stallThreshold = time.Second
	maxStallDumps  = 5
	stackDumpBytes = 1 << 20
)

// stalls counts main-thread stalls longer than stallThreshold (smoke).
var stalls atomic.Int64

// Stalls reports how many main-thread stalls the watchdog saw.
func Stalls() int64 { return stalls.Load() }

// WatchStalls pings the main thread and, when a ping waits longer than a
// second, logs every goroutine's stack (the main thread's shows what
// blocked it). At most a few dumps per launch.
func WatchStalls(log *slog.Logger) {
	go func() {
		dumps := 0

		for {
			time.Sleep(stallPing)

			var answered atomic.Bool

			sent := time.Now()
			objc.Main(func() { answered.Store(true) })

			dumped := false

			for !answered.Load() {
				time.Sleep(stallPing / 10)

				if waited := time.Since(sent); waited > stallThreshold && !dumped {
					dumped = true
					stalls.Add(1)

					if dumps < maxStallDumps {
						dumps++

						buf := make([]byte, stackDumpBytes)
						n := runtime.Stack(buf, true)
						log.Warn("main thread stalled", "waited", waited.Round(time.Millisecond), "stacks", string(buf[:n]))
					}
				}
			}

			if dumped {
				log.Warn("main thread resumed", "after", time.Since(sent).Round(time.Millisecond))
			}
		}
	}()
}
