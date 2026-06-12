package agent

// PromptForIssue selects the skill invocation an agent dispatched for an
// issue should run. Shared by the TUI and GUI dispatch paths so the
// routing policy cannot drift between surfaces.
//
// Bug-ness wins regardless of tracker — a Shortcut bug story still wants
// root-cause analysis, not the generic planner. Otherwise the PM/eng
// split decides: Shortcut tickets are PM and want planning; everything
// else is engineering and goes straight to execution.
func PromptForIssue(trackerKind string, isBug bool, key string) string {
	switch {
	case isBug:
		return "/human-bug-plan " + key
	case trackerKind == "shortcut":
		return "/human-plan " + key
	default:
		return "/human-execute " + key
	}
}
