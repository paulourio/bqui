package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/paulourio/bqui/internal/bq"
)

func TestExplain(t *testing.T) {
	noCreds := errors.New(`bq: list projects: Get "https://bigquery.googleapis.com/bigquery/v2/projects?alt=json": bq: token: adc (find default credentials: google: could not find default credentials. See https://cloud.google.com/docs/authentication/external/set-up-adc for more information) and gcloud print-access-token failed: exec: "gcloud": executable file not found in $PATH`)

	cases := []struct {
		err          error
		title, inDet string
		hint         bool
	}{
		{noCreds, "Not signed in to Google Cloud", "gcloud CLI", true},
		{fmt.Errorf("%w: Syntax error: Unexpected identifier \"SELEC\" at [1:1]", bq.ErrQueryFailed), "Query failed", `Syntax error: Unexpected identifier "SELEC" at [1:1]`, false},
		{errors.New(`googleapi: Error 403: Access Denied: Project bigquery-public-data: User does not have bigquery.jobs.create permission in project bigquery-public-data., accessDenied`), "Can't run jobs in this project", "User does not have bigquery.jobs.create permission", true},
		{errors.New("googleapi: Error 403: Access Denied: Table x: denied, accessDenied"), "Permission denied", "Access Denied: Table x: denied", true},
		{errors.New("disk on fire"), "Something went wrong", "disk on fire", false},
	}

	for _, c := range cases {
		p := Explain(c.err)
		if p.Title != c.title || !strings.Contains(p.Detail, c.inDet) || (p.Hint != "") != c.hint {
			t.Errorf("Explain(%v) = %+v", c.err, p)
		}

		if strings.Contains(p.Detail, "googleapi:") || strings.Contains(p.Detail, "accessDenied") {
			t.Errorf("detail not cleaned: %q", p.Detail)
		}
	}
}
