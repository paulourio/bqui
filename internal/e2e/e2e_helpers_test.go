//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/bqstorage"
)

func requireLiveBQ(t *testing.T) string {
	t.Helper()

	if os.Getenv("BQUI_E2E_LIVE") != "1" {
		t.Skip("set BQUI_E2E_LIVE=1 to run live BigQuery E2E tests")
	}

	for _, env := range []string{"BQUI_E2E_PROJECT", "BQUI_LIVE_PROJECT"} {
		if p := os.Getenv(env); p != "" {
			return p
		}
	}

	return "ff-homelab"
}

func requireVMOrSmokeDir(t *testing.T) (runVM bool, smokeDir string) {
	t.Helper()

	if os.Getenv("BQUI_E2E_VM") == "1" {
		return true, os.Getenv("BQUI_E2E_SMOKE_DIR")
	}

	if dir := os.Getenv("BQUI_E2E_SMOKE_DIR"); dir != "" {
		return false, dir
	}

	t.Skip("set BQUI_E2E_VM=1 or BQUI_E2E_SMOKE_DIR=<dir> to run macOS VM / smoke artifact E2E tests")

	return false, ""
}

func newLiveBQClients(t *testing.T, project string) (*bq.Client, *bqstorage.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)

	client, err := bq.New(ctx, bq.Options{Project: project})
	if err != nil {
		t.Fatalf("bq.New: %v", err)
	}

	sc, err := bqstorage.New(ctx, option.WithTokenSource(client.Tokens()))
	if err != nil {
		t.Fatalf("bqstorage.New: %v", err)
	}

	t.Cleanup(func() { _ = sc.Close() })

	return client, sc
}
