//go:build darwin

package ui

import (
	"slices"
	"strings"

	"github.com/paulourio/bqui/internal/appkit"
)

// maxProjectMenu caps the Add Project submenu (the rest via Other…).
const maxProjectMenu = 50

// addProjectMenu offers the user's projects that are not in the explorer,
// and Other… to type any project ID (e.g. public datasets).
func (s *Shell) addProjectMenu() appkit.MenuItem {
	var items []appkit.MenuItem

	for _, p := range s.all {
		if slices.Contains(s.settings.PinnedProjects, p) {
			continue
		}

		if len(items) == maxProjectMenu {
			items = append(items, appkit.MenuItem{Title: "More projects: use Other…"})

			break
		}

		items = append(items, appkit.MenuItem{Title: p, Run: func() { s.AddProject(p) }})
	}

	if len(items) > 0 {
		items = append(items, appkit.MenuItem{})
	}

	items = append(items, appkit.MenuItem{Title: "Other…", Run: s.promptAddProject})

	return appkit.MenuItem{Title: "Add Project", Submenu: &appkit.Menu{Title: "Add Project", Items: items}}
}

func (s *Shell) promptAddProject() {
	id, ok := appkit.Prompt("Add Project", "Enter a project ID to show in the explorer (for example bigquery-public-data).", "")
	if ok {
		s.AddProject(id)
	}
}

// AddProject pins a project in the explorer (saved) and reveals it.
func (s *Shell) AddProject(project string) {
	project = strings.TrimSpace(strings.ReplaceAll(project, "`", ""))
	if project == "" || strings.ContainsAny(project, " \t/") {
		s.status("✕ Not a project ID: “" + project + "”.")

		return
	}

	if s.settings.Pin(project) {
		s.Explorer.Model.SetProjects(s.settings.PinnedProjects)
		s.rebuildProjectPicker()
		s.saveSettings()
	}

	s.SelectSidebar(SidebarExplorer)

	if id, ok := s.Explorer.Model.Find(project, "", ""); ok {
		s.Explorer.Outline.Select(id)
	}

	s.status("Added " + project + " to the explorer.")
}
