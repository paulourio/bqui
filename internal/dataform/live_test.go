//go:build live

// Live tests talk to the Dataform API. Run with:
//
//	BQUI_LIVE_PROJECT=my-project go test -tags live ./internal/dataform/
package dataform

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/paulourio/bqui/internal/bq"
)

func TestLiveRoundTrip(t *testing.T) {
	project := os.Getenv("BQUI_LIVE_PROJECT")
	if project == "" {
		t.Skip("BQUI_LIVE_PROJECT not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	bqc, err := bq.New(ctx, bq.Options{Project: project})
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(ctx, option.WithTokenSource(bqc.Tokens()))
	if err != nil {
		t.Fatal(err)
	}

	author, err := WhoAmI(ctx, bqc.Tokens())
	t.Logf("author %q (err %v)", author.Name, err)

	list, err := c.List(ctx, project, DefaultRegion)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("%d saved queries", len(list))

	q, sha1, err := c.Create(ctx, project, DefaultRegion, "bqui live test (safe to delete)", "SELECT 1", author)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := c.Delete(context.Background(), q.Name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()

	sql, head, err := c.Read(ctx, q.Name)
	if err != nil || sql != "SELECT 1" || head != sha1 {
		t.Fatalf("read = %q %q (want head %q) %v", sql, head, sha1, err)
	}

	sha2, err := c.Save(ctx, q.Name, "SELECT 2", sha1, author, "Edited in bqui")
	if err != nil {
		t.Fatal(err)
	}

	if h, _ := c.Head(ctx, q.Name); h != sha2 {
		t.Fatalf("head after save = %q, want %q (history order?)", h, sha2)
	}

	// Saving against the stale head must conflict, not overwrite.
	_, err = c.Save(ctx, q.Name, "SELECT 3", sha1, author, "stale")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save = %v", err)
	}

	if sql, _, _ := c.Read(ctx, q.Name); sql != "SELECT 2" {
		t.Fatalf("after conflict = %q", sql)
	}

	if err := c.Rename(ctx, q.Name, "bqui live test renamed"); err != nil {
		t.Fatal(err)
	}

	shared, err := c.Shared(ctx, q.Name)
	t.Logf("shared=%v err=%v", shared, err)

	if err != nil || shared {
		t.Errorf("new query should be private: %v %v", shared, err)
	}
}
