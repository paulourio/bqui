//go:build darwin

package objc

import (
	"fmt"
	"sync"

	pobjc "github.com/ebitengine/purego/objc"
)

// Method describes one Go-implemented Objective-C method. Fn must have the
// signature func(self ID, cmd SEL, args...) [result].
//
// Limitation: purego callbacks cannot return floating-point values, so
// methods returning CGFloat/double (e.g. outlineView:heightOfRowByItem:)
// cannot be implemented in Go; use AppKit alternatives (automatic row
// heights, constraints) instead.
type Method struct {
	Sel string
	Fn  any
}

// RegisterClass defines a new Objective-C class. Each method consumes one
// purego callback slot for the life of the process (slots are limited and
// never freed), so classes must be registered once, never per instance. Use
// LazyClass for that.
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

// HasProtocol reports whether the runtime knows protocol name. Protocols are
// only registered when some loaded binary references them.
func HasProtocol(name string) bool {
	return pobjc.GetProtocol(name) != nil
}

// AvailableProtocols filters names down to the protocols the runtime knows.
// Conformance is advisory for the delegate protocols bqui implements: AppKit
// and WebKit dispatch by selector.
func AvailableProtocols(names ...string) []string {
	out := make([]string, 0, len(names))

	for _, n := range names {
		if HasProtocol(n) {
			out = append(out, n)
		}
	}

	return out
}

// LazyClass registers a class on first use and returns it on every call.
// Protocols the runtime does not know at that moment are skipped (see
// AvailableProtocols). Registration failure is a programming error and panics.
func LazyClass(name, super string, protocols []string, methods func() []Method) func() ID {
	var (
		once sync.Once
		cls  Class
	)

	return func() ID {
		once.Do(func() {
			c, err := RegisterClass(name, super, AvailableProtocols(protocols...), methods())
			if err != nil {
				panic(err)
			}

			cls = c
		})

		return ID(cls)
	}
}

// Registry maps Objective-C instances of a Go-backed class to their Go state.
type Registry[T any] struct {
	mu sync.RWMutex
	m  map[ID]T
}

// Put associates v with id.
func (r *Registry[T]) Put(id ID, v T) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.m == nil {
		r.m = make(map[ID]T)
	}

	r.m[id] = v
}

// Get returns the state associated with id.
func (r *Registry[T]) Get(id ID) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	v, ok := r.m[id]

	return v, ok
}

// Delete removes the state associated with id.
func (r *Registry[T]) Delete(id ID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.m, id)
}

// ActionSelector is the selector sent to targets created by NewTarget.
const ActionSelector = "invoke:"

var (
	targets     Registry[func(sender ID)]
	targetClass = LazyClass("BQUIActionTarget", "NSObject", nil, func() []Method {
		return []Method{{Sel: ActionSelector, Fn: func(self ID, _ SEL, sender ID) {
			if fn, ok := targets.Get(self); ok {
				fn(sender)
			}
		}}}
	})
)

// NewTarget returns an object that calls fn when it receives ActionSelector.
// Use it as the target of controls, menu items and toolbar items. Targets are
// cheap and share one class; they live until ReleaseTarget.
func NewTarget(fn func(sender ID)) ID {
	t := Msg(Msg(targetClass(), "alloc"), "init")
	targets.Put(t, fn)

	return t
}

// ReleaseTarget forgets fn and releases the target.
func ReleaseTarget(t ID) {
	targets.Delete(t)
	Release(t)
}
