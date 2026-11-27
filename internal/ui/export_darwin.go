//go:build darwin

package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/export"
)

// exportChoice is one entry of the Export submenu.
type exportChoice struct {
	title  string
	format export.Format
	opts   export.Options
}

var exportChoices = []exportChoice{
	{"CSV…", export.CSV, export.Options{}},
	{"CSV with JSON Cells…", export.CSV, export.Options{CSVNesting: export.CSVJSONCells}},
	{"JSON Lines…", export.NDJSON, export.Options{}},
	{"Parquet…", export.Parquet, export.Options{}},
}

// exportMenu builds an Export submenu for a source; columnar formats are
// offered only when the Storage Read API can serve the source.
func (s *Shell) exportMenu(title string, src func() (app.ExportSource, string)) appkit.MenuItem {
	items := make([]appkit.MenuItem, 0, len(exportChoices))

	for _, c := range exportChoices {
		if c.format.Columnar() && s.opts.Storage == nil {
			continue
		}

		items = append(items, appkit.MenuItem{Title: c.title, Run: func() {
			source, name := src()
			if source.Table == nil && source.Grid == nil {
				s.status("Nothing to export yet.")

				return
			}

			path, ok := appkit.SaveFile("Export "+c.format.String(), name+"."+c.format.Ext())
			if ok {
				s.ExportTo(source, path, c.format, c.opts)
			}
		}})
	}

	return appkit.MenuItem{Title: title, Submenu: &appkit.Menu{Title: title, Items: items}}
}

// resultsExportSource exports the current results (full destination table
// when available, else the loaded grid).
func (s *Shell) resultsExportSource() (app.ExportSource, string) {
	r := s.Results.Current()
	if r == nil {
		return app.ExportSource{}, "results"
	}

	snap := r.Snapshot()
	src := app.ExportSource{Grid: snap.Grid}
	name := "results"

	if snap.ResultTable != nil && s.opts.Storage != nil {
		src.Table = snap.ResultTable
	}

	if snap.Job != nil {
		name = snap.Job.JobID
	}

	return src, name
}

// tableExportSource exports a whole table from the explorer.
func tableExportSource(info app.ObjectInfo) func() (app.ExportSource, string) {
	return func() (app.ExportSource, string) {
		ref := info.ObjectRef()

		return app.ExportSource{Table: &ref}, info.ObjectRef().Table
	}
}

// ExportTo runs an export and reports progress in the results status line.
func (s *Shell) ExportTo(src app.ExportSource, path string, f export.Format, opts export.Options) {
	if s.exportCancel != nil {
		s.exportCancel()
	}

	name := filepath.Base(path)
	s.status("Exporting " + name + "…")

	s.exportCancel = s.Runner.Export(src, path, f, opts, func(p app.ExportProgress) {
		s.lastExport = p

		switch {
		case !p.Done:
			s.status(exportText("Exporting "+name+"…", p))
		case p.Cancelled:
			s.exportCancel = nil
			s.status("Export cancelled.")
		case p.Err != nil:
			s.exportCancel = nil
			s.status("✕ Export failed: " + p.Err.Error())
		default:
			s.exportCancel = nil
			s.status(exportText("✓ Exported to "+path, p))
		}
	})
}

// LastExport returns the most recent export progress (smoke checks).
func (s *Shell) LastExport() app.ExportProgress { return s.lastExport }

func exportText(prefix string, p app.ExportProgress) string {
	parts := []string{prefix, app.CountInt(p.Rows)}

	if !p.Done && p.EstRows > 0 {
		parts[1] += " of ~" + app.CountInt(p.EstRows)
	}

	parts[1] += " rows"
	parts = append(parts, app.Bytes(p.Bytes)+" written", seconds(p.Elapsed))

	if p.ScannedEst > 0 {
		parts = append(parts, fmt.Sprintf("~%s read (≈$%.2f Storage Read API)", app.Bytes(p.ScannedEst), app.StorageReadUSD(p.ScannedEst)))
	}

	return strings.Join(parts, " · ")
}
