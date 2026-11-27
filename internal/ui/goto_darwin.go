//go:build darwin

package ui

import (
	"slices"
	"strings"
	"time"

	"github.com/paulourio/bqui/internal/app"
	"github.com/paulourio/bqui/internal/objc"
)

const (
	revealStep    = 100 * time.Millisecond
	revealTimeout = 20 * time.Second
)

// objectAt resolves editor references with the billing project and the
// default dataset preference.
func (s *Shell) objectAt(text string, line, character int) (app.ObjectAt, bool) {
	return app.ObjectRefAt(text, line, character, s.BillingProject(), s.settings.Prefs.DefaultDataset)
}

// RevealObject selects "project.dataset.name" in the explorer, loading the
// project and dataset as needed, and shows its schema in the inspector.
// Unpinned projects are added to the explorer for this launch.
func (s *Shell) RevealObject(ref string) {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) != 3 {
		return
	}

	project, dataset, name := parts[0], parts[1], parts[2]
	m := s.Explorer.Model

	s.SelectSidebar(SidebarExplorer)

	if m.StarredOnly() && !slices.Contains(s.settings.Starred, project+"."+dataset) {
		s.Explorer.SetStarredOnly(false)
	}

	if _, ok := m.Find(project, "", ""); !ok {
		m.SetProjects(append(slices.Clone(s.settings.PinnedProjects), project))
		s.status("Showing " + project + " in the explorer for now (pin it from the project menu to keep it).")
	}

	s.revealGen++
	gen := s.revealGen
	deadline := time.Now().Add(revealTimeout)

	var step func()

	step = func() {
		if gen != s.revealGen {
			return // a newer Go to Definition took over
		}

		done, err := s.revealStep(project, dataset, name)

		switch {
		case done:
			return
		case err != "":
			s.status("✕ " + err)

			return
		case time.Now().After(deadline):
			s.status("✕ Could not find " + ref + " in the explorer.")

			return
		}

		time.AfterFunc(revealStep, func() { objc.Main(step) })
	}

	step()
}

// revealStep advances one level (project, dataset, object); done when the
// object is selected.
func (s *Shell) revealStep(project, dataset, name string) (done bool, errText string) {
	m, out := s.Explorer.Model, s.Explorer.Outline

	for _, level := range [][2]string{{"", ""}, {dataset, ""}} {
		id, ok := m.Find(project, level[0], level[1])
		if !ok {
			if loadedParent(m, project, level[0]) {
				return false, "“" + project + "." + dataset + "” was not found."
			}

			return false, ""
		}

		if !m.Loaded(id) {
			m.Expand(id)
			out.Expand(id, false)

			return false, ""
		}

		out.Expand(id, false)
	}

	id, ok := m.Find(project, dataset, name)
	if !ok {
		if ds, _ := m.Find(project, dataset, ""); m.Loaded(ds) {
			return false, "“" + project + "." + dataset + "." + name + "” was not found."
		}

		return false, ""
	}

	if !out.Select(id) {
		return false, ""
	}

	s.Inspector.SelectSegment(segSchema)

	return true, ""
}

// loadedParent reports whether the parent of (project, dataset) finished
// loading, so a missing node really is missing.
func loadedParent(m *app.Explorer, project, dataset string) bool {
	if dataset == "" {
		return false
	}

	id, ok := m.Find(project, "", "")

	return ok && m.Loaded(id)
}
