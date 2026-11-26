//go:build darwin

// Command bqui is a native macOS BigQuery client written in pure Go.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/smoke"
	"github.com/paulourio/bqui/internal/ui"
)

var version = "dev"

func init() {
	// AppKit must run on the process main thread.
	runtime.LockOSThread()
}

func main() {
	smokeDir := flag.String("smoke-test", "", "render the UI offscreen into PNGs, ax.json and report.json in this directory, then exit")
	inspect := flag.Bool("inspect", version == "dev", "enable the Safari Web Inspector for the editor")
	flag.Parse()

	if err := objc.Load(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	objc.WithPool(func() {
		shell, err := ui.NewShell(ui.Options{Deterministic: *smokeDir != "", Inspectable: *inspect})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		appkit.SetDelegate(shell.App, &appkit.AppDelegate{
			DidFinishLaunching: func() {
				if *smokeDir == "" {
					return
				}

				go smoke.Start(shell, *smokeDir, version, func(err error) {
					if err != nil {
						fmt.Fprintln(os.Stderr, err)
						os.Exit(1)
					}

					appkit.Terminate(shell.App)
				})
			},
		})

		appkit.Run(shell.App)
	})
}
