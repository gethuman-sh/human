package daemon

import "testing"

// Reading stdin whenever it merely is not a terminal hangs every forwarded
// command run from a script, CI step, or agent container: those inherit a pipe
// that may never reach EOF. Only an invocation that names stdin as a source may
// read it.
func TestWantsStdin(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"state", "set", "SC-1", "k", "--body-file", "-"}, true},
		{[]string{"marker", "post", "SC-1", "plan", "--body-file=-"}, true},
		{nil, false},
		{[]string{"state", "get", "SC-1", "stage.fix"}, false},
		{[]string{"get", "SC-1"}, false},
		// A path that merely ends in a dash is a file, not the stdin sentinel.
		{[]string{"state", "set", "SC-1", "k", "--body-file", "notes-"}, false},
	}
	for _, c := range cases {
		if got := wantsStdin(c.args); got != c.want {
			t.Errorf("wantsStdin(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// The guard must come first: with no stdin-naming argument, nothing is read, so
// an inherited pipe can never block the call.
func TestReadPipedStdin_SkipsWhenNotRequested(t *testing.T) {
	if got := readPipedStdin([]string{"state", "get", "SC-1", "k"}); got != "" {
		t.Errorf("readPipedStdin read %q for a command that never asked for stdin", got)
	}
}
