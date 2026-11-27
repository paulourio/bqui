//go:build e2e

package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestE2EVMSmokeAndLayoutValidation(t *testing.T) {
	runVM, smokeDir := requireVMOrSmokeDir(t)
	root := repoRoot(t)

	if runVM {
		_, stderr, rc := runSubprocess(t, root, "make", nil, "vm-test")
		if rc != 0 {
			t.Fatalf("make vm-test rc=%d stderr=%s", rc, stderr)
		}

		_, stderr, rc = runSubprocess(t, root, "make", nil, "vm-smoke")
		if rc != 0 {
			t.Fatalf("make vm-smoke rc=%d stderr=%s", rc, stderr)
		}

		if smokeDir == "" {
			smokeDir = filepath.Join(root, "dist", "smoke-vm")
		}
	}

	opts := smokeValidationOpts{
		allowSkipped:        map[string]bool{"inspector-segments": true},
		requireLayoutChecks: true,
		requirePerPanePNGs:  true,
	}

	rep, err := validateSmokeArtifacts(smokeDir, opts)
	if err != nil {
		t.Fatalf("smoke artifact validation in %s: %v", smokeDir, err)
	}

	if rep.Failed != 0 {
		t.Fatalf("report.Failed = %d", rep.Failed)
	}

	checkByName := make(map[string]smokeCheck, len(rep.Checks))
	for _, c := range rep.Checks {
		checkByName[c.Name] = c
	}

	if c, ok := checkByName["studio-list-open"]; !ok || !strings.Contains(c.Detail, "groupExpanded true") {
		t.Fatalf("studio-list-open did not verify Studio group expansion: %+v", c)
	}

	if c, ok := checkByName["narrow-window-900x600"]; !ok ||
		!strings.Contains(c.Detail, "schema") ||
		!strings.Contains(c.Detail, "results") ||
		!strings.Contains(c.Detail, "editor") ||
		!strings.Contains(c.Detail, "explorer") {
		t.Fatalf("narrow-window-900x600 missing tightened pane coverage: %+v", c)
	}

	if !slices.Contains(rep.Screenshots, "pane-graph.png") {
		t.Fatalf("report.Screenshots missing pane-graph.png: %v", rep.Screenshots)
	}

	switch envLog := os.Getenv("BQUI_E2E_SMOKE_LOG"); {
	case envLog != "":
		logBytes, err := os.ReadFile(envLog)
		if err != nil {
			t.Fatalf("read BQUI_E2E_SMOKE_LOG %s: %v", envLog, err)
		}

		if err := validateSmokeLog(logBytes); err != nil {
			t.Fatalf("smoke.log validation failed: %v", err)
		}
	case runVM:
		fetchedLog := filepath.Join(t.TempDir(), "smoke.log")
		_, stderr, rc := runSubprocess(t, root, "scp", nil, "-q", "-P", "2222", "mac@localhost:bqui-dev/smoke.log", fetchedLog)
		if rc != 0 {
			t.Fatalf("scp smoke.log rc=%d stderr=%s", rc, stderr)
		}

		logBytes, err := os.ReadFile(fetchedLog)
		if err != nil {
			t.Fatalf("read fetched smoke.log: %v", err)
		}

		if err := validateSmokeLog(logBytes); err != nil {
			t.Fatalf("smoke.log validation failed: %v", err)
		}
	default:
		logPath := filepath.Join(smokeDir, "smoke.log")
		logBytes, err := os.ReadFile(logPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("read %s: %v", logPath, err)
			}
		} else if err := validateSmokeLog(logBytes); err != nil {
			t.Fatalf("smoke.log validation failed: %v", err)
		}
	}
}

func TestE2EPlanStandaloneTestBinaryPortability(t *testing.T) {
	if os.Getenv("BQUI_E2E_VM") != "1" && os.Getenv("BQUI_E2E_ACCEPTANCE") != "1" {
		t.Skip("set BQUI_E2E_VM=1 or BQUI_E2E_ACCEPTANCE=1 to verify standalone internal/plan test binary portability")
	}

	testBin := filepath.Join(t.TempDir(), "plan.test")
	_, stderr, rc := runSubprocess(t, repoRoot(t), "go", []string{"CGO_ENABLED=0"}, "test", "-c", "-o", testBin, "./internal/plan")
	if rc != 0 {
		t.Fatalf("go test -c ./internal/plan rc=%d stderr=%s", rc, stderr)
	}

	// Execute from an isolated temp directory with no repo testdata/ relative path.
	stdout, stderr, rc := runSubprocess(t, t.TempDir(), testBin, nil, "-test.count=1")
	if rc != 0 {
		t.Fatalf("standalone plan.test failed rc=%d stdout=%s stderr=%s", rc, stdout, stderr)
	}
}
