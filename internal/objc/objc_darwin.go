//go:build darwin

// Package objc is bqui's thin layer over purego's Objective-C runtime
// bindings: framework loading, a selector cache, typed message sends, class
// registration, action targets, and main-thread dispatch through a single
// libdispatch trampoline.
package objc

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	pobjc "github.com/ebitengine/purego/objc"
)

// ID is an Objective-C object pointer.
type ID = pobjc.ID

// SEL is an Objective-C selector.
type SEL = pobjc.SEL

// Class is an Objective-C class pointer.
type Class = pobjc.Class

// Nil is the nil object.
const Nil ID = 0

// Framework paths loaded by Load.
const (
	LibSystem  = "/usr/lib/libSystem.B.dylib"
	Foundation = "/System/Library/Frameworks/Foundation.framework/Foundation"
	AppKit     = "/System/Library/Frameworks/AppKit.framework/AppKit"
)

var (
	loadOnce sync.Once
	errLoad  error

	libsMu sync.Mutex
	libs   = make(map[string]uintptr)

	selMu    sync.RWMutex
	selCache = make(map[string]SEL)
)

// Load dlopens Foundation and AppKit so their classes are registered with the
// Objective-C runtime, and prepares main-thread dispatch. It is idempotent.
func Load() error {
	loadOnce.Do(func() {
		for _, path := range []string{LibSystem, Foundation, AppKit} {
			if _, err := lib(path); err != nil {
				errLoad = err

				return
			}
		}

		errLoad = initDispatch()
	})

	return errLoad
}

func lib(path string) (uintptr, error) {
	libsMu.Lock()
	defer libsMu.Unlock()

	if h, ok := libs[path]; ok {
		return h, nil
	}

	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, fmt.Errorf("objc: dlopen %s: %w", path, err)
	}

	libs[path] = h

	return h, nil
}

// Symbol returns the address of an exported symbol of a loaded library.
func Symbol(path, name string) (uintptr, error) {
	h, err := lib(path)
	if err != nil {
		return 0, err
	}

	addr, err := purego.Dlsym(h, name)
	if err != nil {
		return 0, fmt.Errorf("objc: dlsym %s: %w", name, err)
	}

	return addr, nil
}

// Const returns the object stored in an exported framework constant such as
// NSString *const NSToolbarFlexibleSpaceItemIdentifier.
func Const(path, name string) (ID, error) {
	addr, err := Symbol(path, name)
	if err != nil {
		return Nil, err
	}

	// addr points at the constant's storage; read the object pointer from it.
	// The double indirection keeps go vet's unsafeptr check satisfied.
	return **(**ID)(unsafe.Pointer(&addr)), nil
}

// Sel returns the cached selector for name.
func Sel(name string) SEL {
	selMu.RLock()
	s, ok := selCache[name]
	selMu.RUnlock()

	if ok {
		return s
	}

	s = pobjc.RegisterName(name)

	selMu.Lock()
	selCache[name] = s
	selMu.Unlock()

	return s
}

// Cls returns the class object for name as an ID, ready to receive messages.
func Cls(name string) ID {
	return ID(pobjc.GetClass(name))
}

// New returns [[name alloc] init] (+1 retained).
func New(name string) ID {
	return Msg(Msg(Cls(name), "alloc"), "init")
}

// Msg sends sel to id and returns an object result.
func Msg(id ID, sel string, args ...any) ID {
	return id.Send(Sel(sel), args...)
}

// Send sends sel to id and returns a result of type T (struct returns are
// routed through objc_msgSend_stret on amd64 by purego).
func Send[T any](id ID, sel string, args ...any) T {
	return pobjc.Send[T](id, Sel(sel), args...)
}

// RespondsTo reports whether id responds to sel.
func RespondsTo(id ID, sel string) bool {
	return id != Nil && Send[bool](id, "respondsToSelector:", Sel(sel))
}

// Retain retains id and returns it.
func Retain(id ID) ID {
	if id == Nil {
		return Nil
	}

	return Msg(id, "retain")
}

// Release releases id.
func Release(id ID) {
	if id != Nil {
		Msg(id, "release")
	}
}

// String converts a Go string to an autoreleased NSString.
func String(s string) ID {
	return Msg(Cls("NSString"), "stringWithUTF8String:", s)
}

// GoString converts an NSString to a Go string.
func GoString(s ID) string {
	if s == Nil {
		return ""
	}

	return Send[string](s, "UTF8String")
}

// Number returns an autoreleased NSNumber holding v. Values below 2^55 are
// tagged pointers, so equal values are identical objects and need no
// lifetime management; bqui relies on that for outline item handles.
func Number(v uint64) ID {
	return Msg(Cls("NSNumber"), "numberWithUnsignedLongLong:", v)
}

// Uint64 returns the unsigned value of an NSNumber (0 for nil).
func Uint64(n ID) uint64 {
	if n == Nil {
		return 0
	}

	return Send[uint64](n, "unsignedLongLongValue")
}

// Array builds an autoreleased NSArray from items.
func Array(items ...ID) ID {
	arr := Msg(Cls("NSMutableArray"), "arrayWithCapacity:", uint64(len(items)))
	for _, it := range items {
		Msg(arr, "addObject:", it)
	}

	return arr
}

// ArrayItems returns the elements of an NSArray.
func ArrayItems(arr ID) []ID {
	if arr == Nil {
		return nil
	}

	n := Send[uint64](arr, "count")
	out := make([]ID, 0, n)

	for i := range n {
		out = append(out, Msg(arr, "objectAtIndex:", i))
	}

	return out
}

// WithPool runs fn inside an autorelease pool.
func WithPool(fn func()) {
	pool := New("NSAutoreleasePool")
	defer Msg(pool, "drain")

	fn()
}
