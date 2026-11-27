package app

import (
	"errors"
	"regexp"
	"strings"

	"github.com/paulourio/bqui/internal/bq"
)

// Problem is an error explained for people: a short title, the details,
// and what to do about it.
type Problem struct {
	Title  string
	Detail string
	Hint   string
}

var (
	// requestPrefix is Go's `Get "https://…": ` URL noise.
	requestPrefix = regexp.MustCompile(`(?:Get|Post|Put|Patch|Delete) "https?://[^"]*": `)
	// googleapiPrefix is `googleapi: Error 403: `.
	googleapiPrefix = regexp.MustCompile(`googleapi: Error (\d+): `)
)

const signInHint = "In Terminal, run:  gcloud auth application-default login\nThen try again. bqui uses Application Default Credentials, or the gcloud CLI if it is installed."

// Explain describes an error for the UI.
func Explain(err error) Problem {
	if err == nil {
		return Problem{}
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "could not find default credentials") ||
		strings.Contains(lower, "gcloud") && strings.Contains(lower, "executable file not found"):
		return Problem{
			Title:  "Not signed in to Google Cloud",
			Detail: "No Application Default Credentials were found, and the gcloud CLI is not installed or not on the PATH.",
			Hint:   signInHint,
		}
	case strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "reauthentication") || strings.Contains(lower, "token has been expired"):
		return Problem{Title: "Google Cloud sign-in expired", Detail: clean(msg), Hint: signInHint}
	case strings.Contains(lower, "jobs.create") || strings.Contains(lower, "billing"):
		return Problem{
			Title:  "Can't run jobs in this project",
			Detail: clean(msg),
			Hint:   "Choose a billing project you can run queries in from the project menu in the toolbar.",
		}
	case strings.Contains(lower, "error 403") || strings.Contains(lower, "access denied") || strings.Contains(lower, "permission"):
		return Problem{Title: "Permission denied", Detail: clean(msg), Hint: "Ask for access, or pick another billing project in the toolbar."}
	case errors.Is(err, bq.ErrQueryFailed):
		return Problem{Title: "Query failed", Detail: clean(msg)}
	}

	return Problem{Title: "Something went wrong", Detail: clean(msg)}
}

// clean strips package prefixes, request URLs and API boilerplate.
func clean(msg string) string {
	msg = requestPrefix.ReplaceAllString(msg, "")
	msg = googleapiPrefix.ReplaceAllString(msg, "")

	for _, p := range []string{"bq: query failed: ", "bq: list projects: ", "bq: token: ", "bq: ", "dataform: ", "app: "} {
		msg = strings.ReplaceAll(msg, p, "")
	}

	// Drop trailing ", accessDenied"-style reason codes.
	if i := strings.LastIndex(msg, ", "); i > 0 && !strings.ContainsAny(msg[i+2:], " .") {
		msg = msg[:i]
	}

	return strings.TrimSpace(msg)
}
