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
}

// ResolveTopology resolves the tracker topology from the configured instances.
// The first tracker per role wins so the answer is stable across runs. When no
// tracker declares the pm role, the working tracker is only unambiguous if
// exactly one non-engineering tracker exists — with several candidates PM stays
// nil rather than guessing.
func ResolveTopology(instances []Instance) Topology {
	t := Topology{Mode: "single"}
	for i := range instances {
		switch instances[i].InferRole() {
		case "engineering":
			if t.Engineering == nil {
				t.Engineering = &instances[i]
				t.Mode = "split"
			}
		case "pm":
			if t.PM == nil {
				t.PM = &instances[i]
			}
		}
	}
	if t.PM == nil {
		var candidates []*Instance
		for i := range instances {
			if instances[i].InferRole() != "engineering" {
				candidates = append(candidates, &instances[i])
			}
		}
		if len(candidates) == 1 {
			t.PM = candidates[0]
		}
	}
	return t
}
