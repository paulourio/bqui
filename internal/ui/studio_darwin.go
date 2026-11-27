//go:build darwin

package ui

import (
	"errors"
	"strings"
	"sync"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/dataform"
	"github.com/paulourio/bqui/internal/objc"
)

// studioState holds BigQuery Studio UI state on the Shell.
type studioState struct {
	authorOnce sync.Once
	author     dataform.Author
	lastErr    error // last Studio error (smoke checks)
	pending    int   // Studio calls in flight (smoke checks)
}

// StudioBackend is the BigQuery Studio backend (smoke checks).
func (s *Shell) StudioBackend() app.StudioBackend { return s.opts.Studio }

// StudioBusy reports Studio calls in flight; StudioErr the last error.
func (s *Shell) StudioBusy() bool { return s.studio.pending > 0 }

// StudioErr is the last BigQuery Studio error (nil after a success).
func (s *Shell) StudioErr() error { return s.studio.lastErr }

// studioAuthor resolves the commit author once (off the main thread).
func (s *Shell) studioAuthor() dataform.Author {
	s.studio.authorOnce.Do(func() {
		if s.opts.StudioAuthor != nil {
			s.studio.author = s.opts.StudioAuthor()
		} else {
			s.studio.author = dataform.Author{Name: "bqui", Email: "bqui@users.noreply.invalid"}
		}
	})

	return s.studio.author
}

// studioDo runs fn off the main thread, then done on it with the error.
func (s *Shell) studioDo(busy string, fn func() error, done func(error)) {
	if s.opts.Studio == nil {
		s.status("BigQuery Studio is not available.")

		return
	}

	s.studio.pending++
	s.status(busy)

	go func() {
		err := fn()

		objc.Main(func() {
			s.studio.pending--
			s.studio.lastErr = err

			done(err)
		})
	}()
}

// OpenStudioQuery loads a BigQuery Studio saved query into the editor.
func (s *Shell) OpenStudioQuery(q dataform.SavedQuery) {
	if !s.keepChanges() {
		return
	}

	var sql string

	s.studioDo("Opening “"+q.Title+"” from BigQuery Studio…", func() error {
		ctx, cancel := app.StudioContext()
		defer cancel()

		var err error
		sql, err = s.Workspace.OpenRemote(ctx, s.opts.Studio, q)

		return err
	}, func(err error) {
		if err != nil {
			s.status("✕ " + err.Error())

			return
		}

		s.Editor.SetTextAt(sql, 0, 0)
		s.updateTitle()
		s.status("Opened “" + q.Title + "” from BigQuery Studio.")
		s.Editor.Focus()
	})
}

// SaveStudio commits the editor text to the linked saved query. On a
// conflict it asks whether to overwrite or take BigQuery's version
// (deterministic runs only report it).
func (s *Shell) SaveStudio(overwrite bool) {
	s.studioDo("Saving to BigQuery Studio…", func() error {
		ctx, cancel := app.StudioContext()
		defer cancel()

		return s.Workspace.SaveRemote(ctx, s.opts.Studio, s.studioAuthor(), overwrite)
	}, func(err error) {
		switch {
		case err == nil:
			s.updateTitle()
			s.status("Saved to BigQuery Studio.")
		case errors.Is(err, dataform.ErrConflict):
			s.status("✕ " + err.Error())
			s.resolveConflict()
		default:
			s.status("✕ " + err.Error())
		}
	})
}

func (s *Shell) resolveConflict() {
	if s.opts.Deterministic {
		return
	}

	switch appkit.Confirm("“"+s.Workspace.Tab().Title+"” changed in BigQuery Studio",
		"Someone saved a newer version since you opened it. Overwrite it with your text, or open BigQuery's version (your edits are discarded)?",
		"Overwrite", "Cancel", "Open BigQuery Version") {
	case appkit.AlertFirst:
		s.SaveStudio(true)
	case appkit.AlertThird:
		s.ReloadStudio()
	}
}

// ReloadStudio replaces the editor text with the saved query's current
// version.
func (s *Shell) ReloadStudio() {
	var sql string

	s.studioDo("Reloading from BigQuery Studio…", func() error {
		ctx, cancel := app.StudioContext()
		defer cancel()

		var err error
		sql, err = s.Workspace.ReloadRemote(ctx, s.opts.Studio)

		return err
	}, func(err error) {
		if err != nil {
			s.status("✕ " + err.Error())

			return
		}

		s.Editor.SetTextAt(sql, 0, 0)
		s.updateTitle()
		s.status("Reloaded from BigQuery Studio.")
	})
}

// PublishStudio asks for a title and saves the editor text as a new
// BigQuery Studio saved query (private), linking the tab and its file.
func (s *Shell) PublishStudio() {
	t := s.Workspace.Tab()
	if t.Remote != "" {
		s.SaveStudio(false)

		return
	}

	title, ok := appkit.Prompt("Save to BigQuery Studio", "A private saved query in "+s.BillingProject()+" ("+s.settings.Prefs.Region()+").", s.Workspace.Title())
	if !ok {
		return
	}

	s.PublishStudioAs(title)
}

// PublishStudioAs saves the editor text as a new saved query (smoke checks
// call this directly).
func (s *Shell) PublishStudioAs(title string) {
	title = strings.TrimSpace(title)
	if title == "" || title == "Untitled" {
		title = "Untitled query"
	}

	project, region := s.BillingProject(), s.settings.Prefs.Region()

	s.studioDo("Saving “"+title+"” to BigQuery Studio…", func() error {
		ctx, cancel := app.StudioContext()
		defer cancel()

		_, err := s.Workspace.PublishRemote(ctx, s.opts.Studio, project, region, title, s.studioAuthor())

		return err
	}, func(err error) {
		if err != nil {
			s.status("✕ " + err.Error())

			return
		}

		s.updateTitle()
		s.status("Saved “" + title + "” to BigQuery Studio.")
		s.Workbooks.Reload()
	})
}

// RenameStudioQuery renames a saved query in BigQuery Studio.
func (s *Shell) RenameStudioQuery(q dataform.SavedQuery) {
	title, ok := appkit.Prompt("Rename Saved Query", "", q.Title)
	if !ok || strings.TrimSpace(title) == "" {
		return
	}

	s.studioDo("Renaming…", func() error {
		ctx, cancel := app.StudioContext()
		defer cancel()

		return s.opts.Studio.Rename(ctx, q.Name, strings.TrimSpace(title))
	}, func(err error) {
		if err != nil {
			s.status("✕ " + err.Error())

			return
		}

		s.status("Renamed to “" + strings.TrimSpace(title) + "”.")
		s.Workbooks.ReloadStudio()
	})
}

// DeleteStudioQuery deletes a saved query (after confirmation).
func (s *Shell) DeleteStudioQuery(q dataform.SavedQuery) {
	if appkit.Confirm("Delete “"+q.Title+"” from BigQuery Studio?",
		"This deletes the saved query and its history for everyone it is shared with. It cannot be undone.",
		"Delete", "Cancel") != appkit.AlertFirst {
		return
	}

	s.studioDo("Deleting…", func() error {
		ctx, cancel := app.StudioContext()
		defer cancel()

		return s.opts.Studio.Delete(ctx, q.Name)
	}, func(err error) {
		if err != nil {
			s.status("✕ " + err.Error())

			return
		}

		s.status("Deleted “" + q.Title + "”.")
		s.Workbooks.ReloadStudio()
	})
}
