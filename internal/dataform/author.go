package dataform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// tokenInfoURL describes an access token (email when the token has the
// userinfo.email scope, as gcloud tokens do).
var tokenInfoURL = "https://oauth2.googleapis.com/tokeninfo" //nolint:gosec // G101: an endpoint URL, not a credential

// fallbackAuthor signs commits when the account email is unknown.
var fallbackAuthor = Author{Name: "bqui", Email: "bqui@users.noreply.invalid"}

const whoamiTimeout = 10 * time.Second

// WhoAmI returns the commit author for the credentials (the account
// email), or a neutral author if the token does not reveal one.
func WhoAmI(ctx context.Context, ts oauth2.TokenSource) (Author, error) {
	tok, err := ts.Token()
	if err != nil {
		return fallbackAuthor, fmt.Errorf("dataform: token: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, whoamiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenInfoURL+"?access_token="+url.QueryEscape(tok.AccessToken), nil)
	if err != nil {
		return fallbackAuthor, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fallbackAuthor, fmt.Errorf("dataform: tokeninfo: %w", err)
	}
	defer resp.Body.Close()

	var info struct {
		Email string `json:"email"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Email == "" {
		return fallbackAuthor, nil // no email scope: use the neutral author
	}

	name, _, _ := strings.Cut(info.Email, "@")

	return Author{Name: name, Email: info.Email}, nil
}
