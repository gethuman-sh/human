package daemon

import "testing"

func TestStageAgentNames_oneNamePerOrdinaryStage(t *testing.T) {
	for _, stage := range []BoardStage{BoardPlanning, BoardImplementation, BoardVerification} {
		got := stageAgentNames("SC-1", stage)
		want := []string{agentNameFor("SC-1", stage)}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("stage %s: got %v, want %v", stage, got, want)
		}
	}
}

func TestStageAgentNames_doneStageNamesAllThree(t *testing.T) {
	got := stageAgentNames("SC-1", BoardDoneStage)
	want := []string{"board-SC-1-prreview", "board-SC-1-prfix", "board-SC-1-deployfix"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestLiveStageAgent_returnsTheNameThatIsAlive(t *testing.T) {
	alive := map[string]struct{}{"board-SC-1-deployfix": {}}
	name, ok := liveStageAgent(alive, "SC-1", BoardDoneStage)
	if !ok || name != "board-SC-1-deployfix" {
		t.Fatalf("got (%q, %v), want (board-SC-1-deployfix, true)", name, ok)
	}

	name, ok = liveStageAgent(map[string]struct{}{}, "SC-1", BoardDoneStage)
	if ok || name != "" {
		t.Fatalf("got (%q, %v), want (\"\", false)", name, ok)
	}
}
