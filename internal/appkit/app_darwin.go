//go:build darwin

package appkit

import (
	"github.com/paulourio/bqui/internal/objc"
)

const activationPolicyRegular int64 = 0

// NSEventModifierFlags used for key equivalents.
const (
	ModShift   uint64 = 1 << 17
	ModControl uint64 = 1 << 18
	ModOption  uint64 = 1 << 19
	ModCommand uint64 = 1 << 20
)

// AppDelegate holds the Go callbacks of the NSApplication delegate.
type AppDelegate struct {
	DidFinishLaunching func()
	WillTerminate      func()
}

var (
	appDelegates     objc.Registry[*AppDelegate]
	appDelegateClass = objc.LazyClass("BQUIAppDelegate", "NSObject", []string{"NSApplicationDelegate"}, func() []objc.Method {
		return []objc.Method{
			{Sel: "applicationDidFinishLaunching:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
				if d, ok := appDelegates.Get(self); ok && d.DidFinishLaunching != nil {
					d.DidFinishLaunching()
				}
			}},
			{Sel: "applicationWillTerminate:", Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
				if d, ok := appDelegates.Get(self); ok && d.WillTerminate != nil {
					d.WillTerminate()
				}
			}},
			{Sel: "applicationShouldTerminateAfterLastWindowClosed:", Fn: func(objc.ID, objc.SEL, objc.ID) bool {
				return true
			}},
			{Sel: "applicationSupportsSecureRestorableState:", Fn: func(objc.ID, objc.SEL, objc.ID) bool {
				return true
			}},
		}
	})
)

// SharedApplication returns NSApp configured as a regular (Dock) application.
func SharedApplication() objc.ID {
	app := objc.Msg(objc.Cls("NSApplication"), "sharedApplication")
	objc.Send[bool](app, "setActivationPolicy:", activationPolicyRegular)

	return app
}

// SetDelegate installs d as NSApp's delegate. The delegate lives for the
// whole process.
func SetDelegate(app objc.ID, d *AppDelegate) {
	obj := objc.Msg(objc.Msg(appDelegateClass(), "alloc"), "init")
	appDelegates.Put(obj, d)
	objc.Msg(app, "setDelegate:", obj)
}

// Run activates the app and enters the AppKit run loop. It does not return.
func Run(app objc.ID) {
	objc.Msg(app, "activate")
	objc.Msg(app, "run")
}

// Terminate asks NSApp to terminate.
func Terminate(app objc.ID) {
	objc.Msg(app, "terminate:", objc.Nil)
}
