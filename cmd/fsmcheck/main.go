// Command fsmcheck validates internal/pipelinefsm/pipeline-fsm.json — the pipeline state
// machine written down.
//
// It checks the document against itself: that it is a well-formed machine.
// Every transition comes from and leads to a state that exists, no two states or
// transitions share a name, nothing is unreachable, nothing is a trap with no
// way out, every transition names a declared actor and says where it lives.
// Whether the document is TRUE of the code is a separate question that needs the
// code; this answers whether it holds together well enough to be reasoned about
// at all.
//
//	fsmcheck                 # validate the machine, exit non-zero on findings
//	fsmcheck -format json    # the same findings, for a tool to read
//	fsmcheck -mermaid        # draw the machine instead of validating it
//
// It takes no path. The machine is compiled in, and `go run ./cmd/fsmcheck`
// compiles it from the working tree, so the document checked is always the one
// being edited — without anyone having to name a checkout.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

func main() {
	format := flag.String("format", "text", "findings format: text or json")
	diagram := flag.Bool("mermaid", false, "print the machine as a mermaid state diagram instead of checking it")
	strict := flag.Bool("strict", false, "fail on warnings too, not only on a machine that does not hold together")
	flag.Parse()

	if err := run(*format, *diagram, *strict); err != nil {
		fmt.Fprintln(os.Stderr, "fsmcheck:", err)
		os.Exit(2)
	}
}

// run exits 1 when the machine does not hold together and returns an error when
// the document cannot be parsed (exit 2), so a caller can tell an unreadable
// document from a broken machine.
func run(format string, diagram, strict bool) error {
	if diagram {
		doc, err := pipelinefsm.Load()
		if err != nil {
			return err
		}
		fmt.Print(pipelinefsm.Mermaid(doc))
		return nil
	}

	findings, err := pipelinefsm.Check()
	if err != nil {
		return err
	}
	if err := report(findings, format); err != nil {
		return err
	}
	if failing := pipelinefsm.Errors(findings); failing > 0 || (strict && len(findings) > 0) {
		os.Exit(1)
	}
	return nil
}

func report(findings []pipelinefsm.Finding, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		// An empty slice, not null: a consumer should see "no findings", not a
		// missing answer.
		if findings == nil {
			findings = []pipelinefsm.Finding{}
		}
		return enc.Encode(findings)
	case "text":
		if len(findings) == 0 {
			fmt.Println("internal/pipelinefsm/pipeline-fsm.json is a well-formed machine")
			return nil
		}
		for _, f := range findings {
			fmt.Println(f)
		}
		errs := pipelinefsm.Errors(findings)
		fmt.Printf("\n%d error(s), %d warning(s) in internal/pipelinefsm/pipeline-fsm.json\n", errs, len(findings)-errs)
		return nil
	default:
		return fmt.Errorf("unknown format %q: want text or json", format)
	}
}
