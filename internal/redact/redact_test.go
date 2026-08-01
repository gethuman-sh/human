package redact

import (
	"strings"
	"testing"
)

func TestCollectSecretEnv(t *testing.T) {
	environ := []string{
		"SHORTCUT_HUMAN_TOKEN=supersecretvalue",
		"JIRA_X_KEY=alsosecret99",
		"MY_PASSWORD=hunter2hunter2",
		"SHORT_KEY=tiny",      // below min length: kept out
		"HOME=/home/somebody", // not secret-named
		"PATH=/usr/bin:/bin",  // PAT is segment-matched: PATH stays out
		"NOEQUALS",            // malformed
	}
	vals := CollectSecretEnv(environ)
	for _, want := range []string{"supersecretvalue", "alsosecret99", "hunter2hunter2"} {
		found := false
		for _, v := range vals {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", want, vals)
		}
	}
	for _, v := range vals {
		if v == "tiny" {
			t.Fatal("short value must not be collected")
		}
		if v == "/usr/bin:/bin" {
			t.Fatal("PATH value must not be collected")
		}
	}
}

func TestTextWith(t *testing.T) {
	got := TextWith("push ghp_abcdefghijklmnopqrstu12 with Bearer abc.def.ghi and s3cr3tenvva1", []string{"s3cr3tenvva1"})
	if strings.Contains(got, "ghp_") || strings.Contains(got, "abc.def.ghi") || strings.Contains(got, "s3cr3tenvva1") {
		t.Fatalf("secrets survived sanitization: %q", got)
	}
	if strings.Count(got, "[redacted]") != 3 {
		t.Fatalf("want 3 redactions, got %q", got)
	}
}
