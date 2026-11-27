// Package bq is bqui's BigQuery REST facade: authentication, metadata,
// query execution, and jobs. It wraps the generated BigQuery v2 client so
// bqui keeps access to every raw field the console shows (query plans,
// session info, job creation reasons).
package bq

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/paulourio/bqlang/catalog"
	"golang.org/x/oauth2"
	bigquery "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// OAuth scopes bqui requests. cloud-platform covers BigQuery jobs and the
// Storage Read API.
const (
	ScopeCloudPlatform = "https://www.googleapis.com/auth/cloud-platform"
	ScopeBigQuery      = "https://www.googleapis.com/auth/bigquery"
)

const (
	tokenReuse    = time.Minute
	tokenTimeout  = 30 * time.Second
	gcloudTimeout = 10 * time.Second
)

// ErrNoProject is returned when no billing project can be determined.
var ErrNoProject = errors.New("bq: no project set (use -project, GOOGLE_CLOUD_PROJECT, or gcloud config set project)")

// Options configures a Client.
type Options struct {
	// Project is the default billing project for jobs.
	Project string
	// Location is the default job location ("" lets BigQuery decide).
	Location string
	// TokenProvider supplies bearer tokens. Nil uses bqlang's ADC→gcloud
	// chain with ScopeCloudPlatform.
	TokenProvider catalog.TokenProvider
	// ClientOptions are appended to the REST client options (tests use
	// option.WithEndpoint, option.WithHTTPClient, option.WithoutAuthentication).
	ClientOptions []option.ClientOption
}

// Client talks to the BigQuery REST v2 API.
type Client struct {
	svc      *bigquery.Service
	project  string
	location string
	tokens   oauth2.TokenSource
}

// New creates a Client.
func New(ctx context.Context, opts Options) (*Client, error) {
	tp := opts.TokenProvider
	if tp == nil {
		tp = catalog.NewDefaultTokenProvider(ScopeCloudPlatform)
	}

	ts := TokenSource(tp)
	clientOpts := append([]option.ClientOption{option.WithTokenSource(ts)}, opts.ClientOptions...)

	svc, err := bigquery.NewService(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("bq: new service: %w", err)
	}

	return &Client{svc: svc, project: opts.Project, location: opts.Location, tokens: ts}, nil
}

// Project returns the default billing project.
func (c *Client) Project() string { return c.project }

// Location returns the default job location.
func (c *Client) Location() string { return c.location }

// Tokens returns the OAuth2 token source, for sibling clients (Storage Read API).
func (c *Client) Tokens() oauth2.TokenSource { return c.tokens }

// Service exposes the generated REST service for calls not wrapped here.
func (c *Client) Service() *bigquery.Service { return c.svc }

func (c *Client) projectOr(p string) (string, error) {
	if p != "" {
		return p, nil
	}

	if c.project != "" {
		return c.project, nil
	}

	return "", ErrNoProject
}

// TokenSource adapts a bqlang TokenProvider to oauth2. Tokens are reported
// with a short expiry so the (internally cached) provider is consulted
// regularly and picks up refreshed credentials.
func TokenSource(tp catalog.TokenProvider) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, providerSource{tp: tp})
}

type providerSource struct{ tp catalog.TokenProvider }

func (s providerSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTimeout)
	defer cancel()

	tok, err := s.tp(ctx)
	if err != nil {
		return nil, fmt.Errorf("bq: token: %w", err)
	}

	return &oauth2.Token{AccessToken: tok, TokenType: "Bearer", Expiry: time.Now().Add(tokenReuse)}, nil
}

// DefaultProject returns $GOOGLE_CLOUD_PROJECT, $CLOUDSDK_CORE_PROJECT, or
// the active gcloud configuration's project, in that order.
func DefaultProject(ctx context.Context) string {
	for _, env := range []string{"GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
		if p := os.Getenv(env); p != "" {
			return p
		}
	}

	ctx, cancel := context.WithTimeout(ctx, gcloudTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "gcloud", "config", "get", "project").Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// IsNotFound reports whether err is an HTTP 404 from the API.
func IsNotFound(err error) bool {
	var gerr *googleapi.Error

	return errors.As(err, &gerr) && gerr.Code == http.StatusNotFound
}
