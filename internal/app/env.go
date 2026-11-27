package app

import (
	"path/filepath"
	"strings"
)

// GUIPath builds the PATH for a Finder-launched app, which inherits only
// /usr/bin:/bin:/usr/sbin:/sbin and so cannot find gcloud. The login
// shell's PATH (if known) comes first, then the inherited one, then the
// usual gcloud install locations that exist. Entries are deduplicated.
func GUIPath(current, shellPath, home string, exists func(string) bool) string {
	candidates := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/share/google-cloud-sdk/bin",
		"/usr/local/bin",
		"/usr/local/share/google-cloud-sdk/bin",
	}

	if home != "" {
		candidates = append(candidates, filepath.Join(home, "google-cloud-sdk", "bin"), filepath.Join(home, ".local", "bin"))
	}

	seen := map[string]bool{}

	var out []string

	add := func(dir string, check bool) {
		if dir == "" || seen[dir] || check && !exists(dir) {
			return
		}

		seen[dir] = true
		out = append(out, dir)
	}

	for _, d := range filepath.SplitList(shellPath) {
		add(d, false)
	}

	for _, d := range filepath.SplitList(current) {
		add(d, false)
	}

	for _, d := range candidates {
		add(d, true)
	}

	return strings.Join(out, string(filepath.ListSeparator))
}
