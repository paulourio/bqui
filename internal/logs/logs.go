// Package logs sets up bqui's log file and crash output under
// ~/Library/Logs/bqui, with simple size-based rotation.
package logs

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

const (
	logName   = "bqui.log"
	crashName = "crash.log"
	maxSize   = 5 << 20 // rotate bqui.log beyond this
	keep      = 3       // bqui.log.1 .. bqui.log.3
	dirPerm   = 0o750
	filePerm  = 0o600
)

// DefaultDir is ~/Library/Logs/bqui.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("logs: %w", err)
	}

	return filepath.Join(home, "Library", "Logs", "bqui"), nil
}

// Setup rotates and opens dir/bqui.log, makes it the default slog
// destination (also echoed to stderr when that is a terminal), sends Go
// crash reports to dir/crash.log, and, when stderr is not a terminal (the
// app was launched from Finder), redirects os.Stderr into the log so
// diagnostics are kept. Call the returned func on exit.
func Setup(dir string) (closeFn func(), err error) {
	if err = os.MkdirAll(dir, dirPerm); err != nil {
		return func() {}, fmt.Errorf("logs: %w", err)
	}

	path := filepath.Join(dir, logName)
	rotate(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		return func() {}, fmt.Errorf("logs: %w", err)
	}

	var out io.Writer = f
	if terminal(os.Stderr) {
		out = io.MultiWriter(f, os.Stderr)
	} else {
		os.Stderr = f
	}

	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	logger.Info("bqui started", "pid", os.Getpid(), "time", time.Now().Format(time.RFC3339))

	crash, err := os.OpenFile(filepath.Join(dir, crashName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, filePerm)
	if err == nil {
		_ = debug.SetCrashOutput(crash, debug.CrashOptions{})
	}

	return func() {
		logger.Info("bqui exiting")

		_ = f.Close()

		if crash != nil {
			_ = crash.Close()
		}
	}, nil
}

// rotate shifts bqui.log → .1 → .2 … when it grew past maxSize.
func rotate(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxSize {
		return
	}

	for i := keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}

	_ = os.Rename(path, path+".1")
}

func terminal(f *os.File) bool {
	info, err := f.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
