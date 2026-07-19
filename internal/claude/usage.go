package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// WindowStart returns the start of the current 5-hour usage window in UTC.
func WindowStart(now time.Time) time.Time {
	utc := now.UTC()
	block := utc.Hour() / 5
	return time.Date(utc.Year(), utc.Month(), utc.Day(), block*5, 0, 0, 0, time.UTC)
}

// WindowEnd returns the end of the current 5-hour usage window in UTC.
func WindowEnd(start time.Time) time.Time {
	return start.Add(5 * time.Hour)
}

// jsonlLine is the minimal structure we need from each JSONL line.
type jsonlLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// classifyModel derives a short display name ("opus 4.8", "fable 5") from a
// model id. The family is parsed from the id's shape rather than matched
// against a fixed list, so a new model family labels itself instead of
// falling back to a wrong name; ids that don't look like Claude ids pass
// through verbatim for the same reason.
func classifyModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return "unknown"
	}
	// "claude" may be prefixed (Bedrock: "us.anthropic.claude-…").
	idx := strings.Index(m, "claude")
	if idx < 0 {
		return m
	}
	family, version := parseClaudeID(m[idx:])
	if family == "" {
		return m
	}
	if version == "" {
		return family
	}
	return family + " " + version
}

// parseClaudeID splits "claude-<family>-<version…>" — and the legacy
// version-first shape "claude-3-5-sonnet-…" — into family and a dotted
// version. Date stamps and suffixes like "v1:0" end the version run
// (isVersionDigit rejects them), so they never masquerade as versions.
func parseClaudeID(id string) (family, version string) {
	segments := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '.' || r == ':'
	})
	var pre, post []string
	for _, seg := range segments[1:] { // segments[0] is "claude" itself
		if isVersionDigit(seg) {
			if family == "" {
				pre = append(pre, seg)
			} else {
				post = append(post, seg)
			}
			continue
		}
		if family == "" {
			family = seg
			continue
		}
		break
	}
	digits := post
	if len(digits) == 0 {
		digits = pre
	}
	if len(digits) > 2 {
		digits = digits[:2]
	}
	return family, strings.Join(digits, ".")
}

// isVersionDigit returns true for short numeric strings (1-2 digits)
// that represent version numbers, not date stamps like "20250514".
func isVersionDigit(s string) bool {
	if len(s) == 0 || len(s) > 2 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ModelUsage holds aggregated token counts for one model class.
type ModelUsage struct {
	InputTokens  int
	OutputTokens int
	CacheCreate  int
	CacheRead    int
}

// UsageSummary holds the full usage breakdown for the current window.
type UsageSummary struct {
	Models map[string]*ModelUsage
}

// CalculateUsage scans JSONL files under root and returns usage broken down by model.
func CalculateUsage(walker DirWalker, root string, now time.Time) (*UsageSummary, error) {
	winStart := WindowStart(now)
	winEnd := WindowEnd(winStart)
	summary := &UsageSummary{Models: make(map[string]*ModelUsage)}

	err := walker.WalkJSONL(root, func(line []byte) error {
		var entry jsonlLine
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil // skip malformed lines
		}
		if entry.Type != "assistant" || entry.Message.Usage == nil {
			return nil
		}
		if entry.Timestamp.Before(winStart) || !entry.Timestamp.Before(winEnd) {
			return nil
		}

		model := classifyModel(entry.Message.Model)
		u := entry.Message.Usage

		mu := summary.Models[model]
		if mu == nil {
			mu = &ModelUsage{}
			summary.Models[model] = mu
		}

		mu.InputTokens += u.InputTokens
		mu.OutputTokens += u.OutputTokens
		mu.CacheCreate += u.CacheCreationInputTokens
		mu.CacheRead += u.CacheReadInputTokens
		return nil
	})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "scanning JSONL files", "root", root)
	}
	return summary, nil
}

// TokenHourBucket is one hour's fresh vs cache-read token split. Fresh folds
// input, output and cache-creation tokens (all of which are billed work);
// cache reads are kept apart so the panel can show what the cache saved.
type TokenHourBucket struct {
	Bucket    string `json:"bucket"` // "2006-01-02 15:00" in UTC
	Fresh     int    `json:"fresh"`
	CacheRead int    `json:"cacheRead"`
}

// ClaudeProjectsRoot returns ~/.claude/projects on the local host. The path was
// only ever built inline before (discovery.go); the board's token panel needs
// the projects root without a specific project dir, so it gets a named helper.
func ClaudeProjectsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving home directory for Claude projects root")
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// TokensByHour scans JSONL under root and returns per-hour token buckets whose
// timestamps fall within [since, until], ordered ascending by hour. It reuses
// the same assistant-usage filter as CalculateUsage; only the windowing and the
// fresh/cache split differ. This reads already-recorded local files — it adds
// no new collection.
func TokensByHour(walker DirWalker, root string, since, until time.Time) ([]TokenHourBucket, error) {
	buckets := make(map[string]*TokenHourBucket)

	err := walker.WalkJSONL(root, func(line []byte) error {
		var entry jsonlLine
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil // skip malformed lines
		}
		if entry.Type != "assistant" || entry.Message.Usage == nil {
			return nil
		}
		if entry.Timestamp.Before(since) || entry.Timestamp.After(until) {
			return nil
		}

		// A fixed-width "YYYY-MM-DD HH:00" key sorts lexically == chronologically,
		// so the final sort needs no time parsing.
		key := entry.Timestamp.UTC().Truncate(time.Hour).Format("2006-01-02 15:00")
		b := buckets[key]
		if b == nil {
			b = &TokenHourBucket{Bucket: key}
			buckets[key] = b
		}
		u := entry.Message.Usage
		b.Fresh += u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens
		b.CacheRead += u.CacheReadInputTokens
		return nil
	})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "scanning JSONL for token buckets", "root", root)
	}

	out := make([]TokenHourBucket, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out, nil
}

// TokenScan is the product of a single JSONL pass that answers both questions the
// board's token panel asks: the current 5h window's fresh/cache split (the
// headline) and the per-hour buckets over the selected range (the panel). It
// exists so the daemon's hot path reads the tree once instead of twice.
type TokenScan struct {
	WindowFresh     int               // Input+Output+CacheCreate in the current 5h window
	WindowCacheRead int               // CacheRead in the current 5h window
	PerHour         []TokenHourBucket // per-hour buckets in [since, until], ascending
}

// ScanTokens folds CalculateUsage's current-window totals and TokensByHour's
// per-hour bucketing into a single WalkJSONL pass. Both apply the same
// assistant-usage filter over the same tree; only the two time windows differ —
// the 5h headline window (derived from now) and the range window [since, until].
// Reading the tree once halves the filesystem work per uncached daemon request.
// CalculateUsage and TokensByHour stay in place for CollectInstanceUsage/CLI.
func ScanTokens(walker DirWalker, root string, since, until, now time.Time) (TokenScan, error) {
	winStart := WindowStart(now)
	winEnd := WindowEnd(winStart)
	buckets := make(map[string]*TokenHourBucket)
	var scan TokenScan

	err := walker.WalkJSONL(root, func(line []byte) error {
		var entry jsonlLine
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil // skip malformed lines
		}
		if entry.Type != "assistant" || entry.Message.Usage == nil {
			return nil
		}
		u := entry.Message.Usage

		// Headline: the current 5h window. Mirrors CalculateUsage's fresh split
		// (input+output+cache-create billed as work; cache reads kept apart).
		if !entry.Timestamp.Before(winStart) && entry.Timestamp.Before(winEnd) {
			scan.WindowFresh += u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens
			scan.WindowCacheRead += u.CacheReadInputTokens
		}

		// Panel: per-hour buckets over [since, until]. A fixed-width
		// "YYYY-MM-DD HH:00" key sorts lexically == chronologically.
		if !entry.Timestamp.Before(since) && !entry.Timestamp.After(until) {
			key := entry.Timestamp.UTC().Truncate(time.Hour).Format("2006-01-02 15:00")
			b := buckets[key]
			if b == nil {
				b = &TokenHourBucket{Bucket: key}
				buckets[key] = b
			}
			b.Fresh += u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens
			b.CacheRead += u.CacheReadInputTokens
		}
		return nil
	})
	if err != nil {
		return TokenScan{}, errors.WrapWithDetails(err, "scanning JSONL for token scan", "root", root)
	}

	scan.PerHour = make([]TokenHourBucket, 0, len(buckets))
	for _, b := range buckets {
		scan.PerHour = append(scan.PerHour, *b)
	}
	sort.Slice(scan.PerHour, func(i, j int) bool { return scan.PerHour[i].Bucket < scan.PerHour[j].Bucket })
	return scan, nil
}

func formatBytes(b uint64) string {
	const (
		gib = 1024 * 1024 * 1024
		mib = 1024 * 1024
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gib))
	default:
		return fmt.Sprintf("%.0f MiB", float64(b)/float64(mib))
	}
}

func FormatMemory(mem *MemoryInfo) string {
	if mem == nil {
		return ""
	}
	usage := formatBytes(mem.Usage)
	if mem.Limit > 0 && mem.Limit < 1<<62 {
		return fmt.Sprintf("mem: %s / %s", usage, formatBytes(mem.Limit))
	}
	return fmt.Sprintf("mem: %s", usage)
}

// FormatTokens formats a token count as a human-readable string (e.g. 1.5M, 2.3K).
func FormatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// Total returns the sum of all token fields.
func (mu *ModelUsage) Total() int {
	return mu.InputTokens + mu.OutputTokens + mu.CacheCreate + mu.CacheRead
}

// FormatUsage writes the usage summary to w.
func FormatUsage(w io.Writer, summary *UsageSummary, now time.Time) error {
	ws := WindowStart(now)
	we := WindowEnd(ws)

	_, err := fmt.Fprintf(w, "Claude usage [%02d:00 – %02d:00 UTC]\n", ws.Hour(), we.Hour())
	if err != nil {
		return err
	}

	// Compute grand total for percentage.
	var grandTotal int
	for _, mu := range summary.Models {
		if mu != nil {
			grandTotal += mu.Total()
		}
	}

	// Sort model names for stable output.
	models := make([]string, 0, len(summary.Models))
	for m := range summary.Models {
		models = append(models, m)
	}
	sort.Strings(models)

	for _, model := range models {
		mu, ok := summary.Models[model]
		if !ok || mu == nil {
			continue
		}
		pct := 0.0
		if grandTotal > 0 {
			pct = float64(mu.Total()) / float64(grandTotal) * 100
		}
		_, err := fmt.Fprintf(w, "  %-12s  %4.0f%%  in: %s  out: %s  cache: %s/%s\n",
			model, pct, FormatTokens(mu.InputTokens), FormatTokens(mu.OutputTokens),
			FormatTokens(mu.CacheCreate), FormatTokens(mu.CacheRead))
		if err != nil {
			return err
		}
	}
	return nil
}

// InstanceUsage pairs an Instance with its calculated usage.
type InstanceUsage struct {
	Instance Instance
	Summary  *UsageSummary
	State    InstanceState
}

// CollectInstanceUsage calculates usage for each instance and returns results.
func CollectInstanceUsage(instances []Instance, now time.Time) []InstanceUsage {
	var results []InstanceUsage
	for _, inst := range instances {
		summary, err := CalculateUsage(inst.Walker, inst.Root, now)
		if err != nil {
			continue
		}
		results = append(results, InstanceUsage{Instance: inst, Summary: summary, State: StateUnknown})
	}
	return results
}

// MergeUsage adds all model usage from src into dst.
// Both arguments may be nil; the call is a no-op when src is nil and the
// destination is left untouched.
func MergeUsage(dst, src *UsageSummary) {
	if dst == nil || src == nil {
		return
	}
	for model, srcMU := range src.Models {
		if srcMU == nil {
			continue
		}
		dstMU := dst.Models[model]
		if dstMU == nil {
			dstMU = &ModelUsage{}
			dst.Models[model] = dstMU
		}
		dstMU.InputTokens += srcMU.InputTokens
		dstMU.OutputTokens += srcMU.OutputTokens
		dstMU.CacheCreate += srcMU.CacheCreate
		dstMU.CacheRead += srcMU.CacheRead
	}
}

func FormatModelRows(w io.Writer, summary *UsageSummary, grandTotal int) error {
	models := make([]string, 0, len(summary.Models))
	for m := range summary.Models {
		models = append(models, m)
	}
	sort.Strings(models)

	for _, model := range models {
		mu, ok := summary.Models[model]
		if !ok || mu == nil || mu.Total() == 0 {
			continue
		}
		pct := 0.0
		if grandTotal > 0 {
			pct = float64(mu.Total()) / float64(grandTotal) * 100
		}
		_, err := fmt.Fprintf(w, "  %-12s  %4.0f%%  in: %s  out: %s  cache: %s/%s\n",
			model, pct, FormatTokens(mu.InputTokens), FormatTokens(mu.OutputTokens),
			FormatTokens(mu.CacheCreate), FormatTokens(mu.CacheRead))
		if err != nil {
			return err
		}
	}
	return nil
}

// FormatMultiUsage writes per-instance and aggregated total usage.
func FormatMultiUsage(w io.Writer, instances []InstanceUsage, now time.Time) error {
	ws := WindowStart(now)
	we := WindowEnd(ws)

	if _, err := fmt.Fprintf(w, "Claude usage [%02d:00 – %02d:00 UTC]\n", ws.Hour(), we.Hour()); err != nil {
		return err
	}

	// Compute grand total across all instances for percentages.
	total := &UsageSummary{Models: make(map[string]*ModelUsage)}
	for _, iu := range instances {
		MergeUsage(total, iu.Summary)
	}
	var grandTotal int
	for _, mu := range total.Models {
		if mu != nil {
			grandTotal += mu.Total()
		}
	}

	// Print each instance with per-instance percentages.
	for _, iu := range instances {
		header := fmt.Sprintf("\n%s", iu.Instance.Label)
		if mem := FormatMemory(iu.Instance.Memory); mem != "" {
			header += "  " + mem
		}
		if _, err := fmt.Fprintf(w, "%s\n", header); err != nil {
			return err
		}
		if iu.Summary == nil {
			continue
		}
		var instanceTotal int
		for _, mu := range iu.Summary.Models {
			if mu != nil {
				instanceTotal += mu.Total()
			}
		}
		if err := FormatModelRows(w, iu.Summary, instanceTotal); err != nil {
			return err
		}
	}

	// Print aggregated total.
	if _, err := fmt.Fprintf(w, "\nTotal:\n"); err != nil {
		return err
	}
	return FormatModelRows(w, total, grandTotal)
}
