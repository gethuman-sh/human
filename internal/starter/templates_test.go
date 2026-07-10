package starter

import "testing"

func TestLookupFindsRegisteredTemplate(t *testing.T) {
	tpl, ok := Lookup("web", "go")
	if !ok {
		t.Fatal("expected web/go template to be registered")
	}
	if tpl.ID != "web-go" || tpl.Path != "web/go" {
		t.Fatalf("unexpected template resolved: %+v", tpl)
	}
}

func TestLookupUnknownCombination(t *testing.T) {
	if _, ok := Lookup("web", "rust"); ok {
		t.Fatal("expected unregistered combination to miss")
	}
	if _, ok := Lookup("ios", "swift"); ok {
		t.Fatal("expected unregistered type to miss")
	}
}

// Every registry entry must be fully populated: the frontend renders labels
// verbatim and Fetch needs a repo path, so an empty field is a silent UI or
// download failure waiting to happen when new templates get added.
func TestTemplatesRegistryComplete(t *testing.T) {
	tpls := Templates()
	if len(tpls) == 0 {
		t.Fatal("expected at least one registered template")
	}
	seen := map[string]bool{}
	for _, tpl := range tpls {
		if tpl.ID == "" || tpl.Type == "" || tpl.TypeLabel == "" ||
			tpl.Language == "" || tpl.LanguageLabel == "" || tpl.Path == "" {
			t.Fatalf("template with empty field: %+v", tpl)
		}
		if seen[tpl.ID] {
			t.Fatalf("duplicate template id %q", tpl.ID)
		}
		seen[tpl.ID] = true
	}
}

func TestTemplatesReturnsCopy(t *testing.T) {
	Templates()[0].ID = "mutated"
	if templates[0].ID == "mutated" {
		t.Fatal("Templates() must not expose the registry for mutation")
	}
}
