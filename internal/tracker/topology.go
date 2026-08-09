package tracker

// Topology is the resolved answer to the question every pipeline agent
// otherwise re-derives from `human tracker list` output: which tracker carries
// the PM role, which (if any) carries the engineering role, and therefore
// whether work runs single-tracker or split topology.
type Topology struct {
	// Mode is "single" or "split". Split turns on only via an explicit
	// role: engineering declaration — never inferred from tracker kind
	// ([SC-254]).
	Mode        string
	PM          *Instance
	Engineering *Instance
	// Forges is every configured entry that can open a pull request. Reported
	// here because nothing else reports it: `human tracker list` hides forge
	// entries so an agent cannot pick one as a write target ([SC-1671]), which
	// leaves a forge visible in no view at all.
	Forges []*Instance
	// Notes are resolution facts a reader cannot see in their own config —
	// chiefly which capabilities an entry ended up with when it declared none.
	// Prose rather than codes: this exists to be read by whoever is wondering
	// why a backend is not behaving as they assumed.
	Notes []string
}

// ResolveTopology resolves the tracker topology from the configured instances.
// The first tracker per role wins so the answer is stable across runs. When no
// tracker declares the pm role, the working tracker is only unambiguous if
// exactly one non-engineering tracker exists — with several candidates PM stays
// nil rather than guessing.
func ResolveTopology(instances []Instance) Topology {
	// A forge-only entry (role: forge, or any forges: entry) carries no issue
	// tracker capability at all, so it must never count toward PM candidates
	// or role resolution — otherwise a lone forge entry alongside one real
	// tracker makes the PM fallback see two candidates and resolve to nil
	// ([SC-1671]).
	trackerInstances := TrackerInstances(instances)

	t := Topology{Mode: "single"}
	for i := range trackerInstances {
		switch trackerInstances[i].InferRole() {
		case "engineering":
			if t.Engineering == nil {
				t.Engineering = &trackerInstances[i]
				t.Mode = "split"
			}
		case "pm":
			if t.PM == nil {
				t.PM = &trackerInstances[i]
			}
		}
	}
	if t.PM == nil {
		var candidates []*Instance
		for i := range trackerInstances {
			if trackerInstances[i].InferRole() != "engineering" {
				candidates = append(candidates, &trackerInstances[i])
			}
		}
		if len(candidates) == 1 {
			t.PM = candidates[0]
		}
	}
	// Read off the unfiltered instances: the forge capability is the one thing
	// TrackerInstances just discarded.
	t.Forges = forgeCapable(instances)
	t.Notes = resolutionNotes(instances, t.Forges)
	return t
}

// forgeCapable returns every instance that can open a pull request, whether it
// declared itself a forge or acquired the capability by saying nothing.
func forgeCapable(instances []Instance) []*Instance {
	var out []*Instance
	for i := range instances {
		if instances[i].Forge != nil {
			out = append(out, &instances[i])
		}
	}
	return out
}

// resolutionNotes states what a config decided without being asked.
//
// Both notes exist because the same fact — which capabilities an entry ended up
// with — is invisible in the file that produced it. A GitHub entry that names no
// role gets both capabilities, and the daemon's unattended passes then read that
// silence as "forge credentials" and never ask it for tickets ([SC-2132],
// [SC-3868]); a reader who assumed it was their tracker has no way to discover
// that from the config, only from the absence of their tickets. The reverse gap
// is quieter still: give that entry role: pm and the forge capability disappears
// with it, so `human pr create` has nothing to open a pull request against —
// which is not discovered until a pipeline reaches the PR step and stops.
func resolutionNotes(instances []Instance, forges []*Instance) []string {
	var notes []string
	for i := range instances {
		dual := instances[i].Kind == "github" && instances[i].Role == "" &&
			instances[i].Provider != nil && instances[i].Forge != nil
		if !dual {
			continue
		}
		notes = append(notes, "github \""+instances[i].Name+"\" declares no role: — it is both a tracker and the forge. "+
			"Unattended passes (board listing, reconcile, record sync) treat it as forge credentials and never ask it for tickets. "+
			"Declare role: pm to have its issues listed, or role: forge to say plainly what it is for.")
	}
	if len(forges) == 0 && len(instances) > 0 {
		notes = append(notes, "No forge configured — `human pr create` has nothing to open a pull request against. "+
			"Add a forges: entry, or a githubs: entry with role: forge.")
	}
	return notes
}
