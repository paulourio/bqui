//go:build darwin

package ui

import (
	"errors"
	"path/filepath"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/appkit"
	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/objc"
	"github.com/paulourio/bqui/internal/store"
)

// Sidebar sections.
const (
	SidebarExplorer = iota
	SidebarSaved
)

// sidebar stacks the Explorer and Saved Queries panes under a switcher.
func (s *Shell) sidebar() objc.ID {
	s.Saved = newSavedPane(s)
	s.sideTabs = appkit.NewSegmented([]string{"Explorer", "Saved"}, s.SelectSidebar)
	s.sideViews = []objc.ID{s.Explorer.View, s.Saved.View}

	v := appkit.NewView()
	appkit.UseAutoLayout(s.sideTabs.View)
	objc.Msg(v, "addSubview:", s.sideTabs.View)

	content := appkit.NewView()
	appkit.UseAutoLayout(content)
	objc.Msg(v, "addSubview:", content)

	for _, sv := range s.sideViews {
		appkit.AddFilling(content, sv, 0)
	}

	safeTop := objc.Msg(objc.Msg(v, "safeAreaLayoutGuide"), "topAnchor")
	appkit.Activate(
		appkit.Equal(appkit.Anchor(s.sideTabs.View, "topAnchor"), safeTop, sidebarInset),
		appkit.Equal(appkit.Anchor(s.sideTabs.View, "centerXAnchor"), appkit.Anchor(v, "centerXAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "topAnchor"), appkit.Anchor(s.sideTabs.View, "bottomAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "leadingAnchor"), appkit.Anchor(v, "leadingAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "trailingAnchor"), appkit.Anchor(v, "trailingAnchor"), 0),
		appkit.Equal(appkit.Anchor(content, "bottomAnchor"), appkit.Anchor(v, "bottomAnchor"), 0),
	)

	s.SelectSidebar(SidebarExplorer)

	return v
}

// SelectSidebar switches between Explorer and Saved queries.
func (s *Shell) SelectSidebar(i int) {
	s.sideTabs.Select(i)

	for j, sv := range s.sideViews {
		appkit.SetHidden(sv, j != i)
	}

	if i == SidebarSaved {
		s.Saved.Reload()
	}
}

// restoreWorkspace puts the persisted tab into the editor.
func (s *Shell) restoreWorkspace() {
	t := s.Workspace.Tab()
	s.Editor.SetTextAt(t.SQL, t.CursorLine, t.CursorCol)
	s.updateTitle()

	if on, id := s.Workspace.SessionMode(); on && id != "" {
		s.status("BigQuery session restored: " + id)
	}
}

func (s *Shell) onEdited(text string, line, character int) {
	s.Workspace.Edited(text, line, character)
	s.updateTitle()
}

func (s *Shell) updateTitle() {
	t := s.Workspace.Tab()
	edited := s.Workspace.Dirty() && t.File != ""
	appkit.SetWindowDocument(s.Window, s.Workspace.Title(), edited)
}

// keepChanges asks before replacing edited text; false cancels.
func (s *Shell) keepChanges() bool {
	if !s.Workspace.Dirty() || s.opts.Deterministic {
		return true
	}

	t := s.Workspace.Tab()
	if t.File == "" {
		switch appkit.Confirm("Save this query before replacing it?",
			"The editor text is not saved to a file. It stays in run history only if it was run.",
			"Save As…", "Cancel", "Discard") {
		case appkit.AlertFirst:
			return s.SaveQueryAs()
		case appkit.AlertThird:
			return true
		}

		return false
	}

	switch appkit.Confirm("Save changes to “"+t.Title+"”?", "Your changes will be lost if you don't save them.", "Save", "Cancel", "Don't Save") {
	case appkit.AlertFirst:
		return s.SaveQuery()
	case appkit.AlertThird:
		return true
	}

	return false
}

// NewQuery clears the editor (after offering to save).
func (s *Shell) NewQuery() {
	if !s.keepChanges() {
		return
	}

	s.Workspace.New()
	s.Editor.SetTextAt("", 0, 0)
	s.updateTitle()
	s.Editor.Focus()
}

// ChooseQuery shows an open panel in the saved-queries folder.
func (s *Shell) ChooseQuery() {
	if path, ok := appkit.OpenFile("Open Query", s.Workspace.QueriesDir(), "sql"); ok {
		s.OpenQuery(path)
	}
}

// OpenQuery loads a .sql file into the editor.
func (s *Shell) OpenQuery(path string) {
	if !s.keepChanges() {
		return
	}

	sql, err := s.Workspace.Open(path)
	if err != nil {
		s.status("✕ " + err.Error())

		return
	}

	s.Editor.SetTextAt(sql, 0, 0)
	s.updateTitle()
	s.status("Opened " + filepath.Base(path) + ".")
	s.Editor.Focus()
}

// SaveQuery saves to the linked file, or asks for one; false if not saved.
func (s *Shell) SaveQuery() bool {
	err := s.Workspace.Save()
	if errors.Is(err, app.ErrNoFile) {
		return s.SaveQueryAs()
	}

	return s.saved(err)
}

// SaveQueryAs asks for a file name in the saved-queries folder.
func (s *Shell) SaveQueryAs() bool {
	name := store.QueryFileName(s.Workspace.Tab().Title)
	if s.Workspace.Tab().File == "" {
		name = "query.sql"
	}

	path, ok := appkit.SaveFileIn("Save Query", s.Workspace.QueriesDir(), name)
	if !ok {
		return false
	}

	return s.SaveQueryTo(path)
}

// SaveQueryTo saves and links the editor text to path (smoke checks call
// this directly; menus go through the save panel).
func (s *Shell) SaveQueryTo(path string) bool {
	return s.saved(s.Workspace.SaveAs(path, "", s.BillingProject()))
}

func (s *Shell) saved(err error) bool {
	if err != nil {
		s.status("✕ Save failed: " + err.Error())

		return false
	}

	s.updateTitle()
	s.status("Saved " + filepath.Base(s.Workspace.Tab().File) + ".")

	if s.sideTabs.Selected() == SidebarSaved {
		s.Saved.Reload()
	}

	return true
}

// ToggleSessionMode turns BigQuery session mode on or off for the tab.
func (s *Shell) ToggleSessionMode() {
	on, _ := s.Workspace.SessionMode()
	if on {
		s.EndSession()

		return
	}

	s.Workspace.SetSessionMode(true)
	s.status("Session mode on: the next run starts a BigQuery session; temp tables and variables persist between runs.")
}

// EndSession aborts the BigQuery session (if any) and leaves session mode.
func (s *Shell) EndSession() {
	_, id := s.Workspace.SessionMode()
	s.Workspace.SetSessionMode(false)

	if id == "" {
		s.status("Session mode off.")

		return
	}

	s.Results.Show(s.Runner.Start(bq.QueryRequest{SQL: app.EndSessionSQL, Project: s.BillingProject(), SessionID: id}))
	s.status("Ending BigQuery session " + id + "…")
}

// sessionOn reports session mode (menu check mark).
func (s *Shell) sessionOn() bool {
	on, _ := s.Workspace.SessionMode()

	return on
}

// recordFinished stores a finished run once (session ID, history).
func (s *Shell) recordFinished(r *app.Run, snap app.RunSnapshot) {
	if s.recorded[r] {
		return
	}

	if s.recorded == nil {
		s.recorded = map[*app.Run]bool{}
	}

	s.recorded[r] = true

	if snap.SQL == app.EndSessionSQL {
		s.History.LocalChanged()

		return
	}

	s.Workspace.Finished(snap, s.BillingProject())
	s.History.LocalChanged()
}
