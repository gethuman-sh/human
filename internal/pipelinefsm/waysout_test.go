package pipelinefsm

import "testing"

// An edge whose marker a command posts as part of its own work must hand back
// that command rather than the generic hand-post — following hand-post advice
// on such an edge posts the marker twice (SC-3852).
func TestCommandFor_PrefersTheEdgesOwnCommand(t *testing.T) {
	withCommand := Event{Marker: "[human:deploy-started]", Command: "human deploy <KEY>"}
	if got, want := CommandFor(withCommand, "SC-1"), "human deploy SC-1"; got != want {
		t.Fatalf("CommandFor() = %q, want %q", got, want)
	}

	withoutCommand := Event{Marker: "[human:deploy-started]"}
	if got, want := CommandFor(withoutCommand, "SC-1"), "human marker post SC-1 deploy-started"; got != want {
		t.Fatalf("CommandFor() = %q, want %q", got, want)
	}
}
