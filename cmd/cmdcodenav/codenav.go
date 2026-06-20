// Package cmdcodenav wires the code-navigation engine (internal/codenav) into
// the human CLI as the local "codenav" command tree: index a repository into a
// local SQLite index, then ask structural questions about it (definitions,
// references, call graphs, blast radius, search) — token-frugally, for AI agents
// and humans alike.
package cmdcodenav

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/codenav/graph"
	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/query"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// BuildCodenavCmd creates the "codenav" command tree.
func BuildCodenavCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codenav",
		Short: "Index and navigate code structure (local SQLite, for AI agents)",
		Long: "codenav indexes repositories into a local SQLite database and answers " +
			"structural questions — go-to-definition, find-references, call graphs, " +
			"blast radius, and full-text search — fast, offline, and token-frugal.",
	}
	// --db and --json are shared by every subcommand. --db defaults to
	// ~/.human/codenav.db, overridable per invocation.
	cmd.PersistentFlags().String("db", "", "index database file (default ~/.human/codenav.db)")
	cmd.PersistentFlags().Bool("json", false, "machine-readable JSON output")

	cmd.AddCommand(
		buildIndexCmd(),
		buildProjectsCmd(),
		buildRmCmd(),
		buildStatusCmd(),
		buildSymbolsCmd(),
		buildOutlineCmd(),
		buildOverviewCmd(),
		buildSearchCmd(),
		buildDefCmd(),
		buildRefsCmd(),
		buildCallersCmd(true),
		buildCallersCmd(false),
		buildCallPathCmd(),
		buildImpactCmd(),
		buildRoutesCmd(),
	)
	return cmd
}

// --- shared helpers --------------------------------------------------------

func openStore(cmd *cobra.Command) (*store.Store, error) {
	db, _ := cmd.Flags().GetString("db")
	if db == "" {
		db = codenav.DefaultDBPath()
	}
	return store.Open(db)
}

func dbPath(cmd *cobra.Command) string {
	db, _ := cmd.Flags().GetString("db")
	if db == "" {
		db = codenav.DefaultDBPath()
	}
	return db
}

func jsonOut(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

func emitJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// symLoc formats a symbol's location as file:start-end (or file:start when the
// symbol is a single line) — a precise target for an external editor.
func symLoc(file string, start, end int) string {
	if end > start {
		return fmt.Sprintf("%s:%d-%d", file, start, end)
	}
	return fmt.Sprintf("%s:%d", file, start)
}

// --- index / projects / rm / status ----------------------------------------

func buildIndexCmd() *cobra.Command {
	var name string
	var full bool
	cmd := &cobra.Command{
		Use:   "index <path>",
		Short: "Index or refresh a repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := filepath.Abs(args[0])
			if err != nil {
				return errors.WrapWithDetails(err, "resolving path", "path", args[0])
			}
			proj := name
			if proj == "" {
				proj = filepath.Base(root)
			}
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			scan := index.RepoScan{Project: proj, Root: root}
			backends := index.PickFor(scan)
			if len(backends) == 0 {
				return errors.WithDetails("no indexer matched the repository", "root", root)
			}

			// Skip work when the source is byte-for-byte unchanged since last index.
			sig := index.SourceSignature(scan)
			if !full {
				if old, _ := st.ProjectSig(proj); old != "" && old == sig {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%q unchanged since last index; skipping (use --full to force)\n", proj)
					return nil
				}
			}
			w, err := st.NewWriter(proj, root)
			if err != nil {
				return err
			}
			start := time.Now()
			for _, ix := range backends {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "indexing %s with %s backend…\n", proj, ix.Name())
				if err := ix.Index(context.Background(), scan, w); err != nil {
					_ = w.Rollback()
					return err
				}
			}
			if err := w.Commit(gitRev(root)); err != nil {
				return err
			}
			if err := st.SetProjectSig(proj, sig); err != nil {
				return err
			}
			projs, _ := st.ListProjects()
			for _, p := range projs {
				if p.Name == proj {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "indexed %q: %d symbols, %d edges, %d files in %s\n",
						proj, p.Symbols, p.Edges, p.Files, time.Since(start).Round(time.Millisecond))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (default: directory base)")
	cmd.Flags().BoolVar(&full, "full", false, "force a full re-index even if nothing changed")
	return cmd
}

func buildProjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List indexed repositories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			projs, err := st.ListProjects()
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, projs)
			}
			if len(projs) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no projects indexed")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "PROJECT\tFILES\tSYMBOLS\tEDGES\tINDEXED")
			for _, p := range projs {
				when := "-"
				if !p.IndexedAt.IsZero() {
					when = p.IndexedAt.Format("2006-01-02 15:04")
				}
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\n", p.Name, p.Files, p.Symbols, p.Edges, when)
			}
			return tw.Flush()
		},
	}
}

func buildRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <repo>",
		Short: "Remove a repository from the index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			if err := st.DeleteProject(args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %q\n", args[0])
			return nil
		},
	}
}

func buildStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show index health and totals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			projs, err := st.ListProjects()
			if err != nil {
				return err
			}
			var files, syms, edges int
			for _, p := range projs {
				files += p.Files
				syms += p.Symbols
				edges += p.Edges
			}
			path := dbPath(cmd)
			if jsonOut(cmd) {
				return emitJSON(cmd, map[string]any{
					"db": path, "projects": len(projs), "files": files, "symbols": syms, "edges": edges,
				})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "db:       %s\n", path)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "projects: %d\n", len(projs))
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "files:    %d\n", files)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "symbols:  %d\n", syms)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "edges:    %d\n", edges)
			return nil
		},
	}
}

// --- exploration -----------------------------------------------------------

func buildSymbolsCmd() *cobra.Command {
	var repo, kind string
	var limit int
	cmd := &cobra.Command{
		Use:   "symbols",
		Short: "List defined symbols — the entry point for exploring a repo",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			syms, err := query.ListSymbols(st.DB(), repo, kind, limit)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, syms)
			}
			if len(syms) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no symbols (index a repo first: human codenav index <path>)")
				return nil
			}
			for _, s := range syms {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  (%s)\n    %s:%d\n", s.QName, s.Kind, s.File, s.Line)
			}
			if limit > 0 && len(syms) == limit {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n(showing %d; use --limit 0 for all)\n", limit)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "limit to a project")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (func|method|type|var|const)")
	cmd.Flags().IntVar(&limit, "limit", 200, "max results (0 = all)")
	return cmd
}

func buildOutlineCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "outline <file>",
		Short: "List the symbols defined in a file (signatures, no bodies)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			syms, err := query.Outline(st.DB(), args[0], repo)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, syms)
			}
			if len(syms) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no symbols (check the path, or index the repo first)")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			for _, s := range syms {
				lines := strconv.Itoa(s.Line)
				if s.EndLine > s.Line {
					lines = fmt.Sprintf("%d-%d", s.Line, s.EndLine)
				}
				sig := s.Signature
				if sig == "" {
					sig = s.Name
				}
				_, _ = fmt.Fprintf(tw, "L%s\t%s\t%s\n", lines, s.Kind, sig)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "limit to a project")
	return cmd
}

func buildOverviewCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "overview",
		Short: "Architecture summary: symbol counts by kind + most-called hubs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			ov, err := query.GetOverview(st.DB(), repo, 12)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, ov)
			}
			if len(ov.Kinds) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "nothing indexed (run: human codenav index <path>)")
				return nil
			}
			kinds := make([]string, 0, len(ov.Kinds))
			for k := range ov.Kinds {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "symbols by kind:")
			for _, k := range kinds {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-8s %d\n", k, ov.Kinds[k])
			}
			if len(ov.Hubs) > 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nmost-called (start here):")
				for _, h := range ov.Hubs {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %3d×  %s\n        %s:%d\n", h.Callers, h.QName, h.File, h.Line)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "limit to a project")
	return cmd
}

func buildSearchCmd() *cobra.Command {
	var repo string
	var symbols bool
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search (code bodies by default, --symbols for names)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := strings.Join(args, " ")
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			var hits []query.SearchHit
			if symbols {
				hits, err = query.SearchSymbols(st.DB(), q, repo, limit)
			} else {
				hits, err = query.SearchCode(st.DB(), q, repo, limit)
			}
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, hits)
			}
			if len(hits) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no matches")
				return nil
			}
			for _, h := range hits {
				if symbols {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  (%s)\n    %s:%d\n", h.QName, h.Kind, h.File, h.Line)
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n    %s\n", h.File, strings.TrimSpace(h.Snippet))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "limit to a project")
	cmd.Flags().BoolVar(&symbols, "symbols", false, "search symbol names instead of code")
	cmd.Flags().IntVar(&limit, "limit", 25, "max results")
	return cmd
}

// --- navigation ------------------------------------------------------------

func buildDefCmd() *cobra.Command {
	var outline bool
	cmd := &cobra.Command{
		Use:   "def <name|qname>",
		Short: "Go-to-definition (+ source body; --outline for signature only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			hits, err := query.Def(st.DB(), args[0], !outline)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, hits)
			}
			if len(hits) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "not found")
				return nil
			}
			for _, h := range hits {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  (%s) [%s]\n  %s\n", h.QName, h.Kind, h.Fidelity, symLoc(h.File, h.Line, h.EndLine))
				if h.Signature != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", h.Signature)
				}
				if h.Snippet != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", h.Snippet)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&outline, "outline", false, "signature + location only, no body (token-frugal)")
	return cmd
}

func buildRefsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refs <name|qname>",
		Short: "Find-references (with enclosing symbol + source line)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			refs, err := query.Refs(st.DB(), args[0])
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, refs)
			}
			if len(refs) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no references")
				return nil
			}
			for _, r := range refs {
				loc := fmt.Sprintf("%s:%d:%d", r.File, r.Line, r.Col)
				if r.In != "" {
					loc += "  in " + r.In
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), loc)
				if r.Text != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", r.Text)
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n%d reference(s)\n", len(refs))
			return nil
		},
	}
}

// --- call graph ------------------------------------------------------------

func buildCallersCmd(callers bool) *cobra.Command {
	use, short := "callees <qname>", "Symbols transitively called by the target"
	if callers {
		use, short = "callers <qname>", "Symbols that transitively call the target"
	}
	var depth int
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			var nodes []graph.Node
			if callers {
				nodes, err = query.Callers(st.DB(), args[0], depth)
			} else {
				nodes, err = query.Callees(st.DB(), args[0], depth)
			}
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, nodes)
			}
			if len(nodes) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "none")
				return nil
			}
			for _, n := range nodes {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s%s  (%s)\n    %s:%d\n", strings.Repeat("  ", n.Depth), n.QName, n.Kind, n.File, n.Line)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 5, "max traversal depth")
	return cmd
}

func buildCallPathCmd() *cobra.Command {
	var from, to string
	var maxDepth, limit int
	cmd := &cobra.Command{
		Use:   "callpath --from A --to B",
		Short: "Concrete call paths between two symbols (shortest first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" || to == "" {
				return errors.WithDetails("callpath requires --from and --to")
			}
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			paths, err := query.CallPath(st.DB(), from, to, maxDepth, limit)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, paths)
			}
			if len(paths) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no path found")
				return nil
			}
			for i, p := range paths {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "path %d:\n", i+1)
				for _, n := range p {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  → %s  (%s:%d)\n", n.QName, n.File, n.Line)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "source symbol")
	cmd.Flags().StringVar(&to, "to", "", "target symbol")
	cmd.Flags().IntVar(&maxDepth, "max", 12, "max path depth")
	cmd.Flags().IntVar(&limit, "limit", 8, "max paths")
	return cmd
}

func buildRoutesCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "List detected web routes (method → pattern → handler)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			routes, err := query.ListRoutes(st.DB(), repo)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, routes)
			}
			if len(routes) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no routes detected")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "METHOD\tPATTERN\tHANDLER\tWHERE")
			for _, r := range routes {
				handler := r.Handler
				if handler == "" {
					handler = "(unresolved)"
				}
				where := "-"
				if r.File != "" {
					where = fmt.Sprintf("%s:%d", r.File, r.Line)
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Method, r.Pattern, handler, where)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "limit to a project")
	return cmd
}

// --- impact / change analysis ----------------------------------------------

func buildImpactCmd() *cobra.Command {
	var repo string
	var diff bool
	var depth int
	cmd := &cobra.Command{
		Use:   "impact <qname>",
		Short: "Blast radius: transitive callers of a symbol (or of a git diff with --diff)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			seeds, err := impactSeeds(st, repo, diff, args)
			if err != nil {
				return err
			}
			if len(seeds) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no changed/seed symbols found")
				return nil
			}
			qnames := make([]string, len(seeds))
			for i, s := range seeds {
				qnames[i] = s.QName
			}
			impacted, err := query.Impact(st.DB(), qnames, depth)
			if err != nil {
				return err
			}
			if jsonOut(cmd) {
				return emitJSON(cmd, map[string]any{"seeds": seeds, "impacted": impacted})
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "changed/seed symbols:")
			for _, s := range seeds {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s  (%s)\n    %s:%d\n", s.QName, s.Kind, s.File, s.Line)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nimpacted (transitive callers): %d\n", len(impacted))
			for _, n := range impacted {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s%s  (%s)\n    %s:%d\n", strings.Repeat("  ", n.Depth), n.QName, n.Kind, n.File, n.Line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "project (required for --diff if more than one indexed)")
	cmd.Flags().BoolVar(&diff, "diff", false, "compute impact of uncommitted git changes")
	cmd.Flags().IntVar(&depth, "depth", 8, "max traversal depth")
	return cmd
}

// impactSeeds returns the symbols to seed an impact query — either the symbols
// touched by uncommitted git changes (--diff) or the single named symbol.
func impactSeeds(st *store.Store, repo string, diff bool, args []string) ([]query.SymbolHit, error) {
	if !diff {
		if len(args) < 1 {
			return nil, errors.WithDetails("impact requires a <qname>, or use --diff")
		}
		return query.Def(st.DB(), args[0], false)
	}
	root, name, err := resolveProject(st, repo)
	if err != nil {
		return nil, err
	}
	hunks, err := gitDiffHunks(root)
	if err != nil {
		return nil, err
	}
	var seeds []query.SymbolHit
	seen := map[string]bool{}
	for _, h := range hunks {
		syms, err := query.SymbolsInRange(st.DB(), name, h.file, h.lo, h.hi)
		if err != nil {
			return nil, err
		}
		for _, s := range syms {
			if !seen[s.QName] {
				seen[s.QName] = true
				seeds = append(seeds, s)
			}
		}
	}
	return seeds, nil
}

// resolveProject finds a project's root and name. With no repo given it requires
// exactly one indexed project.
func resolveProject(st *store.Store, repo string) (root, name string, err error) {
	projs, err := st.ListProjects()
	if err != nil {
		return "", "", err
	}
	if repo != "" {
		for _, p := range projs {
			if p.Name == repo {
				return p.Root, p.Name, nil
			}
		}
		return "", "", errors.WithDetails("project not indexed", "repo", repo)
	}
	if len(projs) == 1 {
		return projs[0].Root, projs[0].Name, nil
	}
	return "", "", errors.WithDetails("multiple projects indexed; specify --repo")
}

type diffHunk struct {
	file   string
	lo, hi int
}

// gitDiffHunks returns the changed line ranges (new-file side) of uncommitted
// changes vs HEAD, parsed from a zero-context unified diff.
func gitDiffHunks(root string) ([]diffHunk, error) {
	out, err := exec.Command("git", "-C", root, "diff", "--unified=0", "HEAD").Output() // #nosec G204 -- fixed git argv against a known repo root
	if err != nil {
		return nil, errors.WrapWithDetails(err, "git diff failed (is this a git repo?)", "root", root)
	}
	var hunks []diffHunk
	cur := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			if p == "/dev/null" {
				cur = ""
			} else {
				cur = p
			}
		case strings.HasPrefix(line, "@@") && cur != "":
			if lo, hi := parseHunkRange(line); lo > 0 {
				hunks = append(hunks, diffHunk{cur, lo, hi})
			}
		}
	}
	return hunks, nil
}

// parseHunkRange extracts the new-file line range from "@@ -a,b +c,d @@".
func parseHunkRange(h string) (lo, hi int) {
	for _, f := range strings.Fields(h) {
		if !strings.HasPrefix(f, "+") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(f, "+"), ",", 2)
		if len(parts) == 0 {
			return 0, 0
		}
		c, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0
		}
		d := 1
		if len(parts) == 2 {
			d, _ = strconv.Atoi(parts[1])
		}
		if d == 0 {
			return c, c // pure deletion at line c
		}
		return c, c + d - 1
	}
	return 0, 0
}

// gitRev returns the HEAD sha for root, or "" if not a git repo.
func gitRev(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output() // #nosec G204 -- fixed git argv against a known repo root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
