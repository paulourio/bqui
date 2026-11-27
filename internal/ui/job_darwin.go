//go:build darwin

package ui

import (
	"context"

	bigquery "google.golang.org/api/bigquery/v2"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
)

// JobPane shows "Job information" for the current run.
type JobPane struct {
	View    objc.ID
	Outline *appkit.Outline

	be   app.JobsBackend
	fmt  app.Formatter
	src  *detailsSource
	note objc.ID
	gen  int
}

func newJobPane(be app.JobsBackend) *JobPane {
	p := &JobPane{be: be, src: &detailsSource{}}
	p.Outline = appkit.NewOutline(p.src, appkit.OutlineOptions{
		Columns: []appkit.Column{
			{ID: "key", Title: "Property", Width: 170, MinWidth: 80},
			{ID: "value", Title: "Value", Width: 520, MinWidth: 100},
		},
		AutomaticRowHeights: true,
		Menu: func(item uint64) []appkit.MenuItem {
			r, ok := p.src.rows[item]
			if !ok {
				return nil
			}

			return []appkit.MenuItem{{Title: "Copy Value", Run: func() { appkit.CopyToPasteboard(r.Value) }}}
		},
	})
	p.note = appkit.SecondaryLabel("Run a query to see its job information.")

	p.View = appkit.NewView()
	appkit.AddFilling(p.View, p.Outline.Scroll, 0)
	appkit.AddCentered(p.View, p.note)
	appkit.SetHidden(p.Outline.Scroll, true)

	return p
}

// ShowJob loads and displays a job (main thread).
func (p *JobPane) ShowJob(ref bq.JobRef) {
	p.gen++
	gen := p.gen

	appkit.SetString(p.note, "Loading job information…")
	appkit.SetHidden(p.note, false)
	appkit.SetHidden(p.Outline.Scroll, true)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
		defer cancel()

		job, err := p.be.GetJob(ctx, ref)

		objc.Main(func() { p.apply(gen, job, err) })
	}()
}

// Clear shows the placeholder (runs without a job, e.g. previews).
func (p *JobPane) Clear(msg string) {
	p.gen++
	appkit.SetString(p.note, msg)
	appkit.SetHidden(p.note, false)
	appkit.SetHidden(p.Outline.Scroll, true)
}

// Rows reports visible rows (smoke checks).
func (p *JobPane) Rows() int64 { return p.Outline.Rows() }

func (p *JobPane) apply(gen int, job *bigquery.Job, err error) {
	if gen != p.gen {
		return
	}

	if err != nil {
		appkit.SetString(p.note, "Could not load the job: "+err.Error())

		return
	}

	p.src.set(p.fmt.JobSections(job))
	p.Outline.Reload(0)
	p.Outline.Expand(0, true)
	appkit.SetHidden(p.note, true)
	appkit.SetHidden(p.Outline.Scroll, false)
}
