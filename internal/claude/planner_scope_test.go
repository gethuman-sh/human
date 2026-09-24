package claude

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// SC-5275: the planner used to offer "ship the narrow slice now + follow-on"
// against "full scope" as a DECISION REQUIRED fork, even though the ticket
// already states the scope. That fork is gone, but scope has two other faces
// the planner must still get right: delivery order is never a question for
// the human, and a ticket that over-asks or contradicts itself IS a genuine
// fork that must still reach a person — not be buried in plan markdown.
// These pin both, and that the rule is stated once (Autonomy contract rule 4)
// rather than restated inconsistently elsewhere in the prompt.

func TestPlannerScope_DeliveryOrderIsNeverAFork(t *testing.T) {
	planner := readEmbed(t, "human-planner-agent.md")
	require.Contains(t, planner,
		`"ship the narrow slice now + follow-on ticket for the rest" is not a`,
		"delivery order between narrow-slice-now and full-scope must be named as the removed fork")
	require.Contains(t, planner, "Delivery order is never a question",
		"rule 4 must say plainly that delivery order is never a human decision")
}

// The over-ask/contradiction case must still reach the DECISION REQUIRED
// terminal (rule 2's path) rather than being silently noted in the plan —
// the silent-narrowing failure rule 4's own headline forbids.
func TestPlannerScope_OverAskStillEmitsDecisionRequired(t *testing.T) {
	planner := readEmbed(t, "human-planner-agent.md")

	idx4 := strings.Index(planner, "4. **Never narrow the ticket silently.**")
	require.NotZero(t, idx4, "rule 4 must exist")
	nextRule := strings.Index(planner[idx4:], "\n\nPrefer deciding over asking")
	require.NotEqual(t, -1, nextRule, "rule 4 must be followed by the autonomy contract's closing line")
	rule4 := planner[idx4 : idx4+nextRule]

	require.Contains(t, rule4, "contradict", "rule 4 must name the contradicting-criteria case")
	require.Contains(t, rule4, "DECISION REQUIRED", "rule 4 must route the over-ask/contradiction case through the DECISION REQUIRED terminal, not just a plan note")
	require.Contains(t, rule4, "must be amended", "rule 4 must still say the ticket needs amending")
}

// Criterion 3: ONE rule about scope. The plan-format guidance (Architecture
// Decisions) and the Principles list must point at rule 4 rather than
// restating their own version of where the objection goes.
func TestPlannerScope_StatedOnceNotRestated(t *testing.T) {
	planner := readEmbed(t, "human-planner-agent.md")

	require.NotContains(t, planner, "it is a ticket amendment (Autonomy contract rule 4), never a fork the plan raises",
		"the Architecture Decisions guidance must not restate scope handling as a flat ticket-amendment-only statement now that over-ask is a DECISION REQUIRED fork")
	require.NotContains(t, planner, "Reducing what the ticket asks for is never the plan's call: say so on the ticket",
		"the Principles list must cross-reference rule 4 rather than restate a second, inconsistent version of the scope rule")

	// Both surviving mentions must point back at rule 4 as the single source.
	require.Regexp(t, `(?s)Delivery order is never such a decision.*Autonomy contract rule 4`, planner)
	require.Regexp(t, `(?s)Scope is governed by Autonomy contract rule 4, not decided here`, planner)
}

// `human plan defer --help` must not teach back a trigger this PR deletes.
func TestPlanDefer_HelpNamesTheSurvivingTrigger(t *testing.T) {
	raw, err := os.ReadFile("../../cmd/cmdplan/plan.go")
	require.NoError(t, err)
	body := string(raw)
	require.NotContains(t, body, `DECISION REQUIRED fork resolves to "ship the narrow slice`,
		"plan.go must not describe human plan defer as triggered by the planner's own DECISION REQUIRED fork — that fork no longer exists")
	require.Contains(t, body, "[human:option-chosen]",
		"plan.go must describe the surviving trigger: a deferral a person sanctioned via [human:option-chosen]")
}
