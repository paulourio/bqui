//go:build darwin

package appkit

import (
	"fmt"
	"os"
	"runtime/debug"
)

// delegateDepth counts active table/outline delegate callbacks (main thread
// only). Mutating a table while one runs triggers AppKit's "reentrant
// operation in its NSTableView delegate" warning (a future assertion), so
// such calls are reported with their Go stack.
var delegateDepth int

func enterDelegate() func() {
	delegateDepth++

	return func() { delegateDepth-- }
}

func checkReentrancy(op string) {
	if delegateDepth > 0 {
		fmt.Fprintf(os.Stderr, "bqui: table mutation %q inside a delegate callback:\n%s\n", op, debug.Stack())
	}
}
