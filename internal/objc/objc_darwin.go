//go:build darwin

// Package objc is bqui's thin layer over purego's Objective-C runtime
// bindings: framework loading, a selector cache, typed message sends, and
// main-thread dispatch through a single libdispatch trampoline.
package objc

import (
	"fmt"
	"sync"

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

const (
	libSystemPath  = "/usr/lib/libSystem.B.dylib"
	foundationPath = "/System/Library/Frameworks/Foundation.framework/Foundation"
	appKitPath     = "/System/Library/Frameworks/AppKit.framework/AppKit"
)

var (
	loadOnce sync.Once
	errLoad  error

	selMu    sync.RWMutex
	selCache = make(map[string]SEL)
)

// Load dlopens Foundation and AppKit so their classes are registered with the
// Objective-C runtime, and prepares main-thread dispatch. It is idempotent.
func Load() error {
	loadOnce.Do(func() {
		for _, path := range []string{foundationPath, appKitPath} {
			if _, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
				errLoad = fmt.Errorf("objc: dlopen %s: %w", path, err)

				return
			}
		}

		errLoad = initDispatch()
	})

	return errLoad
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

// Msg sends sel to id and returns an object result.
func Msg(id ID, sel string, args ...any) ID {
	return id.Send(Sel(sel), args...)
}

// Send sends sel to id and returns a result of type T (struct returns are
// routed through objc_msgSend_stret on amd64 by purego).
func Send[T any](id ID, sel string, args ...any) T {
	return pobjc.Send[T](id, Sel(sel), args...)
}

// Method describes one Go-implemented Objective-C method. Fn must have the
// signature func(self ID, cmd SEL, args...) [result].
type Method struct {
	Sel string
	Fn  any
}

// RegisterClass defines a new Objective-C class. Classes must be registered
// once per process: every method consumes a purego callback slot, and those
// slots are limited and never freed.
func RegisterClass(name, super string, protocols []string, methods []Method) (Class, error) {
	protos := make([]*pobjc.Protocol, 0, len(protocols))

	for _, p := range protocols {
		proto := pobjc.GetProtocol(p)
		if proto == nil {
			return 0, fmt.Errorf("objc: unknown protocol %q", p)
		}

		protos = append(protos, proto)
	}

	defs := make([]pobjc.MethodDef, 0, len(methods))
	for _, m := range methods {
		defs = append(defs, pobjc.MethodDef{Cmd: Sel(m.Sel), Fn: m.Fn})
	}

	cls, err := pobjc.RegisterClass(name, pobjc.GetClass(super), protos, nil, defs)
	if err != nil {
		return 0, fmt.Errorf("objc: register class %s: %w", name, err)
	}

	return cls, nil
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

// WithPool runs fn inside an autorelease pool.
func WithPool(fn func()) {
	pool := Msg(Msg(Cls("NSAutoreleasePool"), "alloc"), "init")
	defer Msg(pool, "drain")

	fn()
}
