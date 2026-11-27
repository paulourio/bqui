package app

import "testing"

func TestGUIPath(t *testing.T) {
	exists := func(d string) bool { return d == "/opt/homebrew/bin" || d == "/Users/me/google-cloud-sdk/bin" }

	got := GUIPath("/usr/bin:/bin", "", "/Users/me", exists)
	if got != "/usr/bin:/bin:/opt/homebrew/bin:/Users/me/google-cloud-sdk/bin" {
		t.Errorf("no shell PATH = %q", got)
	}

	got = GUIPath("/usr/bin:/bin", "/custom/bin:/usr/bin", "/Users/me", exists)
	if got != "/custom/bin:/usr/bin:/bin:/opt/homebrew/bin:/Users/me/google-cloud-sdk/bin" {
		t.Errorf("with shell PATH = %q", got)
	}
}
