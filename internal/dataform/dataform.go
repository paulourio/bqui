// Package dataform reads and writes BigQuery Studio saved queries.
//
// Representation (verified against the console, 2026-11; see README.md):
// every saved query is a Dataform repository labeled
// single-file-asset-type=sql whose displayName is the title (titles need not
// be unique). Its default branch holds one file, content.sql; each console
// save is a commit. There are no workspaces. Visibility is the repository
// IAM policy (a private query grants roles/dataform.admin to its creator
// only). BigQuery's "US" multi-region stores them in us-central1.
package dataform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/dataform/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	// AssetLabel marks repositories that are BigQuery Studio code assets.
	AssetLabel = "single-file-asset-type"
	// SQLAsset is AssetLabel's value for saved queries.
	SQLAsset = "sql"
	// ContentFile holds the query text.
	ContentFile = "content.sql"
	// DefaultRegion is where the console keeps saved queries for the US
	// multi-region.
	DefaultRegion = "us-central1"
	pageSize      = 200
)

// Errors.
var (
	// ErrConflict means the query changed in BigQuery since it was read.
	ErrConflict = errors.New("dataform: the saved query changed in BigQuery since it was opened")
	// ErrNotFound means the saved query no longer exists.
	ErrNotFound = errors.New("dataform: saved query not found (deleted in BigQuery?)")
)

// SavedQuery is one BigQuery Studio saved query.
type SavedQuery struct {
	Name     string // projects/p/locations/l/repositories/id
	ID       string
	Project  string
	Region   string
	Title    string
	Created  time.Time
	Modified time.Time
}

// Author signs commits.
type Author struct{ Name, Email string }

// Client talks to the Dataform API.
type Client struct {
	svc *dataform.Service
}

// New creates a client (pass option.WithTokenSource with cloud-platform
// scope).
func New(ctx context.Context, opts ...option.ClientOption) (*Client, error) {
	svc, err := dataform.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("dataform: %w", err)
	}

	return &Client{svc: svc}, nil
}

// List returns the saved queries in a project and region, by title.
func (c *Client) List(ctx context.Context, project, region string) ([]SavedQuery, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", project, region)

	var out []SavedQuery

	err := c.svc.Projects.Locations.Repositories.List(parent).PageSize(pageSize).
		Pages(ctx, func(resp *dataform.ListRepositoriesResponse) error {
			for _, r := range resp.Repositories {
				if r.Labels[AssetLabel] == SQLAsset {
					out = append(out, fromRepo(r))
				}
			}

			return nil
		})
	if err != nil {
		return nil, explain(err, project)
	}

	slices.SortFunc(out, func(a, b SavedQuery) int {
		if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
			return c
		}

		return b.Modified.Compare(a.Modified)
	})

	return out, nil
}

// Read returns the query text and the head commit it was read at.
func (c *Client) Read(ctx context.Context, name string) (sql, head string, err error) {
	head, err = c.Head(ctx, name)
	if err != nil {
		return "", "", err
	}

	call := c.svc.Projects.Locations.Repositories.ReadFile(name).Path(ContentFile)
	if head != "" {
		call = call.CommitSha(head)
	}

	resp, err := call.Context(ctx).Do()
	if err != nil {
		return "", "", explain(err, "")
	}

	b, err := base64.StdEncoding.DecodeString(resp.Contents)
	if err != nil {
		return "", "", fmt.Errorf("dataform: content: %w", err)
	}

	return string(b), head, nil
}

// Head returns the newest commit SHA ("" for an empty repository).
func (c *Client) Head(ctx context.Context, name string) (string, error) {
	resp, err := c.svc.Projects.Locations.Repositories.FetchHistory(name).PageSize(1).Context(ctx).Do()
	if err != nil {
		return "", explain(err, "")
	}

	if len(resp.Commits) == 0 {
		return "", nil
	}

	return resp.Commits[0].CommitSha, nil
}

// Save commits new text. base is the commit the text was based on; if the
// query moved on since, Save returns ErrConflict and changes nothing.
func (c *Client) Save(ctx context.Context, name, sql, base string, author Author, message string) (string, error) {
	req := &dataform.CommitRepositoryChangesRequest{
		CommitMetadata: &dataform.CommitMetadata{
			Author:        &dataform.CommitAuthor{Name: author.Name, EmailAddress: author.Email},
			CommitMessage: message,
		},
		RequiredHeadCommitSha: base,
		FileOperations: map[string]dataform.FileOperation{
			ContentFile: {WriteFile: &dataform.WriteFile{Contents: base64.StdEncoding.EncodeToString([]byte(sql))}},
		},
	}

	resp, err := c.svc.Projects.Locations.Repositories.Commit(name, req).Context(ctx).Do()
	if err != nil {
		if isConflict(err) {
			return "", ErrConflict
		}

		return "", explain(err, "")
	}

	return resp.CommitSha, nil
}

// Create makes a new private saved query and commits its text.
func (c *Client) Create(ctx context.Context, project, region, title, sql string, author Author) (SavedQuery, string, error) {
	parent := fmt.Sprintf("projects/%s/locations/%s", project, region)
	repo := &dataform.Repository{
		DisplayName:               title,
		Labels:                    map[string]string{AssetLabel: SQLAsset},
		SetAuthenticatedUserAdmin: true,
	}

	r, err := c.svc.Projects.Locations.Repositories.Create(parent, repo).RepositoryId(newID()).Context(ctx).Do()
	if err != nil {
		return SavedQuery{}, "", explain(err, project)
	}

	q := fromRepo(r)

	sha, err := c.Save(ctx, q.Name, sql, "", author, "Created")
	if err != nil {
		return q, "", err
	}

	return q, sha, nil
}

// Rename changes the title.
func (c *Client) Rename(ctx context.Context, name, title string) error {
	_, err := c.svc.Projects.Locations.Repositories.Patch(name, &dataform.Repository{DisplayName: title}).
		UpdateMask("displayName").Context(ctx).Do()

	return explain(err, "")
}

// Delete removes a saved query (and its history).
func (c *Client) Delete(ctx context.Context, name string) error {
	_, err := c.svc.Projects.Locations.Repositories.Delete(name).Force(true).Context(ctx).Do()

	return explain(err, "")
}

// Shared reports whether anyone besides one user can access the query.
func (c *Client) Shared(ctx context.Context, name string) (bool, error) {
	p, err := c.svc.Projects.Locations.Repositories.GetIamPolicy(name).Context(ctx).Do()
	if err != nil {
		return false, explain(err, "")
	}

	members := map[string]bool{}

	for _, b := range p.Bindings {
		for _, m := range b.Members {
			members[m] = true
		}
	}

	return len(members) > 1, nil
}

func fromRepo(r *dataform.Repository) SavedQuery {
	q := SavedQuery{Name: r.Name, Title: r.DisplayName}

	// projects/{p}/locations/{l}/repositories/{id}
	if parts := strings.Split(r.Name, "/"); len(parts) == 6 {
		q.Project, q.Region, q.ID = parts[1], parts[3], parts[5]
	}

	q.Created, _ = time.Parse(time.RFC3339Nano, r.CreateTime)
	q.Modified = q.Created

	var meta struct {
		LastModified string `json:"last_modified_time"`
	}

	if json.Unmarshal([]byte(r.InternalMetadata), &meta) == nil {
		if t, err := time.Parse(time.RFC3339Nano, meta.LastModified); err == nil {
			q.Modified = t
		}
	}

	if q.Title == "" {
		q.Title = "Untitled query"
	}

	return q
}

// newID is a random UUID (the console names repositories by UUID).
func newID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// conflictMessage is how Dataform rejects a stale requiredHeadCommitSha
// (HTTP 400 failedPrecondition, verified live).
const conflictMessage = "does not match actual HEAD commit SHA"

func isConflict(err error) bool {
	var ge *googleapi.Error

	return errors.As(err, &ge) && ge.Code == http.StatusBadRequest && strings.Contains(ge.Message, conflictMessage)
}

// explain turns API errors into actionable messages.
func explain(err error, project string) error {
	if err == nil {
		return nil
	}

	var ge *googleapi.Error
	if !errors.As(err, &ge) {
		return fmt.Errorf("dataform: %w", err)
	}

	msg := ge.Message

	switch {
	case ge.Code == http.StatusNotFound:
		return ErrNotFound
	case strings.Contains(msg, "SERVICE_DISABLED") || strings.Contains(msg, "has not been used") || strings.Contains(msg, "is disabled"):
		return fmt.Errorf("dataform: the Dataform API is disabled for %s; enable it with `gcloud services enable dataform.googleapis.com --project=%s`: %w", project, project, err)
	case ge.Code == http.StatusForbidden && strings.Contains(msg, "Location"):
		return fmt.Errorf("dataform: no saved queries location %q here; BigQuery Studio keeps US saved queries in %s: %w", locationOf(msg), DefaultRegion, err)
	case ge.Code == http.StatusForbidden:
		return fmt.Errorf("dataform: permission denied; saved queries need roles/dataform.codeEditor (or codeCreator to create): %w", err)
	}

	return fmt.Errorf("dataform: %w", err)
}

func locationOf(msg string) string {
	f := strings.Fields(msg)
	for i, w := range f {
		if w == "Location" && i+1 < len(f) {
			return f[i+1]
		}
	}

	return ""
}
