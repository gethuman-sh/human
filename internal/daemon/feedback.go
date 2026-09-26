package daemon

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/tracker"
)

// FeedbackRunner turns one prompt into one answer with no tools and no memory
// of earlier calls. The launch-time advice is built by exactly one such call,
// so the interface is the smallest thing a host model invocation can be and a
// test can fake it with a function.
type FeedbackRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// FeedbackRunnerFunc adapts a function to FeedbackRunner.
type FeedbackRunnerFunc func(ctx context.Context, prompt string) (string, error)

// Run implements FeedbackRunner.
func (f FeedbackRunnerFunc) Run(ctx context.Context, prompt string) (string, error) { return f(ctx, prompt) }

// FeedbackRecord is the slice of the review record the advice is built from.
// It is its own interface rather than recall.FindingsReader widened, so the
// file-scoped command's fakes stay untouched and this package names only the
// four questions it asks.
type FeedbackRecord interface {
	FindingsForFiles(ctx context.Context, project string, files []string, limit int) ([]recall.ReviewFinding, error)
	FindingClassCounts(ctx context.Context, project string, since time.Time) (map[string]int, error)
	RecentFindings(ctx context.Context, project string, limit int) ([]recall.ReviewFinding, error)
	LatestFindingID(ctx context.Context, project string) (int64, error)
}

// Feedback budgets. They are constants rather than config because the block
// is advice: a project that wants a different shape changes what its record
// says, not how much of it the launch reads.
const (
	// FeedbackTimeout bounds the model call. Advice that is not back by then
	// is dropped and the stage launches without it — a launch must never wait
	// on its own preamble.
	FeedbackTimeout = 30 * time.Second
	// feedbackFileRows caps the rows read for the files the stage will touch.
	feedbackFileRows = 60
	// feedbackRecentRows caps the project-wide rows added after the file rows.
	feedbackRecentRows = 140
	// feedbackClassWindow is how far back the per-class counts look.
	feedbackClassWindow = 90 * 24 * time.Hour
	// feedbackTextRunes bounds one finding's text in the prompt so a single
	// long review cannot crowd out the rest of the record.
	feedbackTextRunes = 300
	// feedbackNone is the answer the model is told to give when the record
	// says nothing relevant, so an empty block and a failed call look the same
	// to the launcher: no block.
	feedbackNone = "NONE"
	// feedbackCacheEntries bounds the in-memory cache; past it the cache is
	// dropped whole, which costs one extra model call per stale key and no
	// bookkeeping.
	feedbackCacheEntries = 256
)

// FeedbackDeps builds the advice block a stage launch carries (SC-5959): the
// project's review record, read at launch time and distilled by one small
// model call into a few lines the agent is told to be aware of. Nothing is
// stored; the only state is a cache keyed on the record's high-water mark so a
// relaunch with no new finding pays for no second call.
//
// Every field except Record and Runner is optional. A nil Record or Runner
// disables the whole thing and Advice returns "", which is the package's
// nil-disables convention and what keeps every existing launch-prompt test
// byte-identical.
type FeedbackDeps struct {
	Record  FeedbackRecord
	Runner  FeedbackRunner
	Project string
	// Workspace is the checkout the scope reads: a path token from a ticket
	// or plan counts only when it exists there, and the PR stages' diff runs
	// in it.
	Workspace string
	// Ticket supplies the title and description the planning scope tokenises.
	// nil leaves the planning scope empty.
	Ticket tracker.Getter
	// Comments supplies the thread the implementation scope reads the plan
	// from. nil leaves that scope empty.
	Comments tracker.Commenter
	// ChangedFiles lists the files a branch changes against the base; nil
	// uses git in Workspace (gitChangedFiles). Injected so the scope is
	// testable without a repository.
	ChangedFiles func(ctx context.Context, workspace, branch string) ([]string, error)
	Timeout      time.Duration
	Now          func() time.Time
	Logger       zerolog.Logger

	mu    sync.Mutex
	cache map[feedbackKey]string
}

type feedbackKey struct {
	key   string
	stage BoardStage
	id    int64
}

// Advice returns the block for one launch, or "" when there is none: no
// record, no rows, a disabled dependency, or a call that failed or timed out.
// It never returns an error, by contract — a failure here is logged and the
// launch proceeds, because advice that could block work would be a gate, and
// this is not one.
func (f *FeedbackDeps) Advice(ctx context.Context, key string, stage BoardStage, branch string) string {
	if f == nil || f.Record == nil || f.Runner == nil {
		return ""
	}
	latest, err := f.Record.LatestFindingID(ctx, f.Project)
	if err != nil {
		f.Logger.Warn().Err(err).Str("pm", key).Msg("launch advice: record unreadable; launching without it")
		return ""
	}
	if latest == 0 {
		return ""
	}
	ck := feedbackKey{key: key, stage: stage, id: latest}
	if block, ok := f.cached(ck); ok {
		return block
	}
	block := f.build(ctx, key, stage, branch)
	f.remember(ck, block)
	return block
}

func (f *FeedbackDeps) cached(k feedbackKey) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	block, ok := f.cache[k]
	return block, ok
}

func (f *FeedbackDeps) remember(k feedbackKey, block string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cache == nil || len(f.cache) >= feedbackCacheEntries {
		f.cache = map[feedbackKey]string{}
	}
	f.cache[k] = block
}

// build reads the record for one launch and asks the model once.
func (f *FeedbackDeps) build(ctx context.Context, key string, stage BoardStage, branch string) string {
	scope := f.scope(ctx, key, stage, branch)
	rows, counts, err := f.gather(ctx, scope.files)
	if err != nil {
		f.Logger.Warn().Err(err).Str("pm", key).Msg("launch advice: record unreadable; launching without it")
		return ""
	}
	if len(rows) == 0 {
		return ""
	}
	prompt := feedbackPrompt(key, scope.title, stage, scope.files, rows, counts)
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = FeedbackTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := f.Runner.Run(callCtx, prompt)
	if err != nil {
		f.Logger.Warn().Err(err).Str("pm", key).Str("stage", string(stage)).
			Msg("launch advice: model call failed; launching without it")
		return ""
	}
	return cleanFeedback(answer)
}

// gather reads the file-scoped rows first and fills the remainder of the row
// budget project-wide, deduplicating on identity so a scoped row is never
// listed twice.
func (f *FeedbackDeps) gather(ctx context.Context, files []string) ([]recall.ReviewFinding, map[string]int, error) {
	var rows []recall.ReviewFinding
	if len(files) > 0 {
		normalized := make([]string, 0, len(files))
		for _, p := range files {
			normalized = append(normalized, NormalizeFindingFile(p))
		}
		scoped, err := f.Record.FindingsForFiles(ctx, f.Project, normalized, feedbackFileRows)
		if err != nil {
			return nil, nil, err
		}
		rows = scoped
	}
	recent, err := f.Record.RecentFindings(ctx, f.Project, feedbackRecentRows)
	if err != nil {
		return nil, nil, err
	}
	rows = appendUnseenFindings(rows, recent)
	counts, err := f.Record.FindingClassCounts(ctx, f.Project, f.now().Add(-feedbackClassWindow))
	if err != nil {
		return nil, nil, err
	}
	return rows, counts, nil
}

func (f *FeedbackDeps) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func appendUnseenFindings(rows, more []recall.ReviewFinding) []recall.ReviewFinding {
	type ident struct {
		key, file, slug string
		pr, round       int
	}
	seen := make(map[ident]bool, len(rows))
	for _, r := range rows {
		seen[ident{r.Key, r.File, r.Slug, r.PR, r.Round}] = true
	}
	for _, r := range more {
		id := ident{r.Key, r.File, r.Slug, r.PR, r.Round}
		if seen[id] {
			continue
		}
		seen[id] = true
		rows = append(rows, r)
	}
	return rows
}

// feedbackPrompt is the whole instruction. It is generic on purpose: it names
// no project, file or class, so the same prompt serves every project the
// daemon runs and the only thing that differs is the record it is given.
func feedbackPrompt(key, title string, stage BoardStage, files []string, rows []recall.ReviewFinding, counts map[string]int) string {
	var b strings.Builder
	b.WriteString("You write a short briefing for an automated engineering agent that is about to run the stage \"")
	b.WriteString(string(stage))
	b.WriteString("\" on ticket ")
	b.WriteString(key)
	if title != "" {
		b.WriteString(" (\"")
		b.WriteString(title)
		b.WriteString("\")")
	}
	b.WriteString(".\n\nBelow is what earlier machine code reviews in this project found, with the file, the class of finding, the ticket and round, and what the fixer did about it. ")
	b.WriteString("Write at most ten lines of advice the agent should be aware of before it changes anything. ")
	b.WriteString("Each line names a file or a class of finding and says what tends to go wrong and what past fixers did. ")
	b.WriteString("Prefer findings in the files this stage is about to touch, then classes that recur across tickets. ")
	b.WriteString("Never quote a finding's text; paraphrase. No preamble, no headings, plain lines starting with \"- \". ")
	b.WriteString("If nothing here is relevant to this stage, answer exactly ")
	b.WriteString(feedbackNone)
	b.WriteString(".\n")
	if len(files) > 0 {
		b.WriteString("\nFiles this stage is likely to touch:\n")
		for _, p := range files {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	if len(counts) > 0 {
		b.WriteString("\nFindings per class in this project, last 90 days:\n")
		for _, c := range sortedClassCounts(counts) {
			fmt.Fprintf(&b, "- %s: %d\n", c.class, c.n)
		}
	}
	b.WriteString("\nRecorded findings, newest first:\n")
	for _, r := range rows {
		b.WriteString(feedbackRow(r))
	}
	return b.String()
}

type classCount struct {
	class string
	n     int
}

func sortedClassCounts(counts map[string]int) []classCount {
	out := make([]classCount, 0, len(counts))
	for class, n := range counts {
		if class == "" {
			class = "(unclassified)"
		}
		out = append(out, classCount{class, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].class < out[j].class
	})
	return out
}

func feedbackRow(r recall.ReviewFinding) string {
	class := r.Class
	if class == "" {
		class = "unclassified"
	}
	disposition := r.Disposition
	if disposition == "" {
		disposition = "no fixer report yet"
	}
	line := fmt.Sprintf("- %s [%s] %s round %d, fixer: %s", r.File, class, r.Key, r.Round, disposition)
	if text := cutRunes(strings.TrimSpace(r.Text), feedbackTextRunes); text != "" {
		line += " — " + text
	}
	if note := cutRunes(strings.TrimSpace(r.Note), feedbackTextRunes); note != "" {
		line += " (fixer: " + note + ")"
	}
	return line + "\n"
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// cleanFeedback normalises the model's answer: the sentinel and blank answers
// become "", surrounding whitespace goes, and any code fence the model wrapped
// the lines in is stripped so the block lands in the prompt as plain lines.
func cleanFeedback(answer string) string {
	s := strings.TrimSpace(answer)
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, feedbackNone) || strings.EqualFold(strings.TrimSuffix(s, "."), feedbackNone) {
		return ""
	}
	return s
}

// FeedbackHeading introduces the block in a launch prompt. It follows the
// dispatch line after a blank line, so the first line — the one skills parse
// their arguments from — stays exactly what it was.
const FeedbackHeading = "## Be aware of these before you change anything"

// withFeedback appends the block to a dispatch prompt; an empty block returns
// the prompt unchanged.
func withFeedback(prompt, block string) string {
	block = strings.TrimSpace(block)
	if block == "" {
		return prompt
	}
	return strings.TrimRight(prompt, "\n") + "\n\n" + FeedbackHeading + "\n\n" + block + "\n"
}
