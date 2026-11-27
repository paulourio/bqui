package dataform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/option"
)

// fakeAPI mimics the Dataform responses recorded live (see README.md).
type fakeAPI struct {
	mu      sync.Mutex
	content map[string]string   // repo name → content.sql
	heads   map[string][]string // repo name → commits, newest first
	titles  map[string]string
	next    int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{content: map[string]string{}, heads: map[string][]string{}, titles: map[string]string{}}
}

const parent = "projects/p/locations/us-central1"

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	name, verb, _ := strings.Cut(path, ":")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }

	switch {
	case r.Method == http.MethodGet && name == parent+"/repositories":
		repos := []map[string]any{{"name": parent + "/repositories/other", "displayName": "a workflow repo"}}
		for n, title := range f.titles {
			repos = append(repos, map[string]any{
				"name": n, "displayName": title, "labels": map[string]string{AssetLabel: SQLAsset},
				"createTime":       "2026-11-27T09:42:18.389054217Z",
				"internalMetadata": `{"last_modified_time":"2026-11-27T09:42:18.663192Z","state":"ACTIVE"}`,
			})
		}
		reply(map[string]any{"repositories": repos})
	case r.Method == http.MethodPost && name == parent+"/repositories":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body["setAuthenticatedUserAdmin"] != true {
			http.Error(w, `{"error":{"code":400,"message":"expected private create"}}`, http.StatusBadRequest)

			return
		}

		n := parent + "/repositories/" + r.URL.Query().Get("repositoryId")
		f.titles[n] = body["displayName"].(string)
		reply(map[string]any{"name": n, "displayName": f.titles[n], "labels": body["labels"]})
	case verb == "fetchHistory":
		commits := []map[string]string{}
		for _, sha := range f.heads[name] {
			commits = append(commits, map[string]string{"commitSha": sha})
		}
		reply(map[string]any{"commits": commits})
	case verb == "readFile":
		if _, ok := f.titles[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})

			return
		}
		reply(map[string]string{"contents": base64.StdEncoding.EncodeToString([]byte(f.content[name]))})
	case verb == "commit":
		var body struct {
			RequiredHeadCommitSha string
			FileOperations        map[string]struct{ WriteFile struct{ Contents string } }
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		head := ""
		if h := f.heads[name]; len(h) > 0 {
			head = h[0]
		}

		if body.RequiredHeadCommitSha != "" && body.RequiredHeadCommitSha != head {
			w.WriteHeader(http.StatusBadRequest)
			reply(map[string]any{"error": map[string]any{
				"code": 400, "status": "FAILED_PRECONDITION",
				"message": fmt.Sprintf("Required HEAD commit SHA %s does not match actual HEAD commit SHA %s", body.RequiredHeadCommitSha, head),
			}})

			return
		}

		b, _ := base64.StdEncoding.DecodeString(body.FileOperations[ContentFile].WriteFile.Contents)
		f.next++
		sha := fmt.Sprintf("sha%d", f.next)
		f.content[name] = string(b)
		f.heads[name] = append([]string{sha}, f.heads[name]...)
		reply(map[string]string{"commitSha": sha})
	case r.Method == http.MethodPatch:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.titles[name] = body["displayName"]
		reply(map[string]string{"name": name})
	case r.Method == http.MethodDelete:
		delete(f.titles, name)
		reply(map[string]any{})
	default:
		w.WriteHeader(http.StatusForbidden)
		reply(map[string]any{"error": map[string]any{"code": 403, "message": "Location us is not found or access is unauthorized."}})
	}
}

func testClient(t *testing.T) (*Client, *fakeAPI) {
	t.Helper()

	api := newFakeAPI()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	c, err := New(context.Background(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}

	return c, api
}

func TestRoundTripAndConflict(t *testing.T) {
	ctx := context.Background()
	c, api := testClient(t)
	me := Author{Name: "me", Email: "me@example.com"}

	q, sha1, err := c.Create(ctx, "p", DefaultRegion, "Top words", "SELECT 1", me)
	if err != nil || q.Project != "p" || q.Region != DefaultRegion || len(q.ID) != 36 {
		t.Fatalf("create = %+v %q %v", q, sha1, err)
	}

	list, err := c.List(ctx, "p", DefaultRegion)
	if err != nil || len(list) != 1 || list[0].Title != "Top words" || list[0].Modified.IsZero() {
		t.Fatalf("list (only sql assets) = %+v %v", list, err)
	}

	sql, head, err := c.Read(ctx, q.Name)
	if err != nil || sql != "SELECT 1" || head != sha1 {
		t.Fatalf("read = %q %q %v", sql, head, err)
	}

	// Someone saves in the console; our stale save must not overwrite it.
	api.mu.Lock()
	api.content[q.Name], api.heads[q.Name] = "SELECT 'console'", append([]string{"console"}, api.heads[q.Name]...)
	api.mu.Unlock()

	if _, err := c.Save(ctx, q.Name, "SELECT 2", sha1, me, "edit"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save = %v", err)
	}

	if sql, _, _ := c.Read(ctx, q.Name); sql != "SELECT 'console'" {
		t.Fatalf("overwritten: %q", sql)
	}

	if _, err := c.Save(ctx, q.Name, "SELECT 2", "console", me, "edit"); err != nil {
		t.Fatal(err)
	}

	if err := c.Rename(ctx, q.Name, "Renamed"); err != nil {
		t.Fatal(err)
	}

	if err := c.Delete(ctx, q.Name); err != nil {
		t.Fatal(err)
	}

	if _, _, err := c.Read(ctx, q.Name); !errors.Is(err, ErrNotFound) {
		t.Errorf("read deleted = %v", err)
	}

	if _, err := c.List(ctx, "p", "us"); err == nil || !strings.Contains(err.Error(), DefaultRegion) {
		t.Errorf("wrong region error = %v", err)
	}
}
