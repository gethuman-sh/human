package forge

import "testing"

func TestIsForgeKind(t *testing.T) {
	tests := map[string]bool{
		"github":      true,
		"gitlab":      false, // not implemented yet (stubbed as a follow-up)
		"jira":        false,
		"linear":      false,
		"shortcut":    false,
		"clickup":     false,
		"azuredevops": false,
		"":            false,
	}
	for kind, want := range tests {
		if got := IsForgeKind(kind); got != want {
			t.Errorf("IsForgeKind(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestKindForHost(t *testing.T) {
	tests := map[string]string{
		"github.com": "github",
		"GitHub.com": "github", // case-insensitive
		"gitlab.com": "",       // not yet supported
		"example.com": "",
		"":            "",
	}
	for host, want := range tests {
		if got := KindForHost(host); got != want {
			t.Errorf("KindForHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantHost string
		wantRepo string
		wantOK   bool
	}{
		{"https with .git", "https://github.com/octocat/hello-world.git", "github.com", "octocat/hello-world", true},
		{"https without .git", "https://github.com/octocat/hello-world", "github.com", "octocat/hello-world", true},
		{"https trailing slash", "https://github.com/octocat/hello-world/", "github.com", "octocat/hello-world", true},
		{"scp-style", "git@github.com:octocat/hello-world.git", "github.com", "octocat/hello-world", true},
		{"scp-style no .git", "git@github.com:octocat/hello-world", "github.com", "octocat/hello-world", true},
		{"ssh scheme", "ssh://git@github.com/octocat/hello-world.git", "github.com", "octocat/hello-world", true},
		{"https with port", "https://github.com:443/octocat/hello-world.git", "github.com", "octocat/hello-world", true},
		{"surrounding space", "  https://github.com/octocat/hello-world.git\n", "github.com", "octocat/hello-world", true},
		{"empty", "", "", "", false},
		{"garbage", "not a url", "", "", false},
		{"scheme no host", "file:///local/path", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, repo, ok := ParseRemoteURL(tt.raw)
			if ok != tt.wantOK || host != tt.wantHost || repo != tt.wantRepo {
				t.Errorf("ParseRemoteURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.raw, host, repo, ok, tt.wantHost, tt.wantRepo, tt.wantOK)
			}
		})
	}
}
