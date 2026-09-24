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

// nothing-to-do's `reason` field closes a set of values (marker.go's spec for
// it). CommandFor must render that set as the placeholder, not the bare field
// name: a bare `<reason>` reads as free text, an asker fills in something
// plausible (e.g. the ticket-review verdict word `superseded`), and Validate
// then refuses it — the exact failure CommandFor's doc comment says it exists
// to move earlier (SC-5326).
func TestCommandFor_RendersTheClosedSetForNothingToDo(t *testing.T) {
	e := Event{Marker: "[human:nothing-to-do]"}
	got := CommandFor(e, "SC-1")
	want := "human marker post SC-1 nothing-to-do --field evidence=<evidence> --field reason=<merged|duplicate|escalated|rejected>"
	if got != want {
		t.Fatalf("CommandFor() = %q, want %q", got, want)
	}
}
