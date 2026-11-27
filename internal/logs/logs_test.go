package logs

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRotatesAndLogs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logName)

	// An oversized log and existing rotations shift by one.
	if err := os.WriteFile(path, make([]byte, maxSize), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(path+".1", []byte("old1"), 0o600)

	stderr, logger := os.Stderr, slog.Default()
	t.Cleanup(func() { os.Stderr = stderr; slog.SetDefault(logger) })

	closeFn, err := Setup(dir)
	if err != nil {
		t.Fatal(err)
	}

	slog.Default().Info("hello from test")
	fmt.Fprintln(os.Stderr, "stderr line") // go test's stderr is not a terminal here or is; either way it must not fail
	closeFn()

	if b, _ := os.ReadFile(path + ".2"); string(b) != "old1" {
		t.Errorf(".2 = %q", b)
	}

	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != maxSize {
		t.Errorf(".1 = %v %v", info, err)
	}

	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "bqui started") || !strings.Contains(string(b), "hello from test") {
		t.Errorf("log = %q", b)
	}

	if _, err := os.Stat(filepath.Join(dir, crashName)); err != nil {
		t.Errorf("crash log: %v", err)
	}
}
