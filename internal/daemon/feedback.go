package daemon

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
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
func (f FeedbackRunnerFunc) Run(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

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
	// feedbackMaxLines is the bound the prompt states and cleanFeedback
	// enforces, so a model that does not count still yields a short block.
	feedbackMaxLines = 10
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
	// Cache spares a relaunch the second model call. It is shared across the
	// per-request FeedbackDeps a daemon builds for one project, so it lives
	// outside them; nil caches nothing, which is what a test wants.
	Cache   *FeedbackCache
	Timeout time.Duration
	Now     func() time.Time
	Logger  zerolog.Logger
}

// FeedbackCache holds rendered blocks keyed on the launch and the record's
// high-water mark, so a relaunch with no new finding pays nothing and a new
// row is a new briefing.
type FeedbackCache struct {
	mu     sync.Mutex
	blocks map[feedbackKey]string
}

// feedbackKey identifies one launch's cached block. branch is part of the
// identity, not an afterthought: the pull-request stages scope by the
// branch's diff (feedback_scope.go's branchFiles), so a block built for one
// branch is not the block a launch on a different branch — or no branch at
// all — would be given. Without it, `human feedback` asked with a differing
// or absent --branch would write into the exact cache slot the next real
// launch reads and serve it a briefing scoped to the wrong diff (SC-6016).
type feedbackKey struct {
	project string
	key     string
	stage   BoardStage
	branch  string
	id      int64
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
	ck := feedbackKey{project: f.Project, key: key, stage: stage, branch: branch, id: latest}
	if block, ok := f.Cache.get(ck); ok {
		return block
	}
	block := f.build(ctx, key, stage, branch)
	f.Cache.put(ck, block)
	return block
}

func (c *FeedbackCache) get(k feedbackKey) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	block, ok := c.blocks[k]
	return block, ok
}

func (c *FeedbackCache) put(k feedbackKey, block string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocks == nil || len(c.blocks) >= feedbackCacheEntries {
		c.blocks = map[feedbackKey]string{}
	}
	c.blocks[k] = block
}

// build reads the record for one launch and asks the model once. The whole
// method — scope (ticket/comment reads and, for the PR stages, a git fetch),
// gather and the model call — runs under one FeedbackTimeout-bounded context
// so a stalled remote call cannot hold a launch past the budget the launch
// prompt itself advertises: build used to bound only the model call, leaving
// scope's ticket, comment and git-fetch reads on the caller's own context —
// context.Background() from ApplyTransition — unbounded (SC-5959).
func (f *FeedbackDeps) build(ctx context.Context, key string, stage BoardStage, branch string) string {
	ctx, cancel := f.bounded(ctx)
	defer cancel()

	rep, err := f.compose(ctx, key, stage, branch)
	if err != nil {
		f.Logger.Warn().Err(err).Str("pm", key).Msg("launch advice: record unreadable; launching without it")
		return ""
	}
	if rep.Rows == 0 {
		return ""
	}
	block, err := f.distill(ctx, rep.Prompt)
	if err != nil {
		f.Logger.Warn().Err(err).Str("pm", key).Str("stage", string(stage)).
			Msg("launch advice: model call failed; launching without it")
		return ""
	}
	return block
}

// Explain answers `human feedback <KEY> <STAGE>` (SC-6016): the block the
// daemon would append when launching that stage now, with the scope and the
// prompt it was distilled from. It shares build's scope, gather and prompt so
// the command shows exactly what a launch sees, and it reads the same cache,
// so asking after a launch costs no second model call. Unlike Advice it
// returns its errors: a person asking wants "the record is unreadable", not a
// silent empty answer.
func (f *FeedbackDeps) Explain(ctx context.Context, key string, stage BoardStage, branch string) (FeedbackReport, error) {
	if f == nil || f.Record == nil || f.Runner == nil {
		return FeedbackReport{}, errors.WithDetails("the launch briefing is disabled on this daemon: no review record or no model runner", "pm", key)
	}
	ctx, cancel := f.bounded(ctx)
	defer cancel()

	latest, err := f.Record.LatestFindingID(ctx, f.Project)
	if err != nil {
		return FeedbackReport{}, errors.WrapWithDetails(err, "reading the review record", "pm", key)
	}
	rep, err := f.compose(ctx, key, stage, branch)
	if err != nil {
		return FeedbackReport{}, err
	}
	if rep.Rows == 0 {
		return rep, nil
	}
	ck := feedbackKey{project: f.Project, key: key, stage: stage, branch: branch, id: latest}
	if block, ok := f.Cache.get(ck); ok {
		rep.Block, rep.Cached = block, true
		return rep, nil
	}
	block, err := f.distill(ctx, rep.Prompt)
	if err != nil {
		return rep, err
	}
	f.Cache.put(ck, block)
	rep.Block = block
	return rep, nil
}

// bounded puts one launch's whole read-and-distill under the deps' timeout.
func (f *FeedbackDeps) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = FeedbackTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// compose does everything before the model: the stage's scope, the record
// rows and counts, and the prompt those become. Rows is 0 when the record
// has nothing, and then Prompt is empty too — there is nothing to ask.
func (f *FeedbackDeps) compose(ctx context.Context, key string, stage BoardStage, branch string) (FeedbackReport, error) {
	scope := f.scope(ctx, key, stage, branch)
	rep := FeedbackReport{Key: key, Stage: stage, Branch: branch, Title: scope.title, Files: scope.files}
	rows, counts, err := f.gather(ctx, scope.files)
	if err != nil {
		return FeedbackReport{}, errors.WrapWithDetails(err, "reading the review record", "pm", key)
	}
	rep.Rows = len(rows)
	if len(rows) == 0 {
		return rep, nil
	}
	rep.Prompt = feedbackPrompt(key, scope.title, stage, scope.files, rows, counts)
	return rep, nil
}

// distill is the one model call, cleaned; "" when the model answered NONE.
func (f *FeedbackDeps) distill(ctx context.Context, prompt string) (string, error) {
	answer, err := f.Runner.Run(ctx, prompt)
	if err != nil {
		return "", errors.WrapWithDetails(err, "distilling the briefing")
	}
	return cleanFeedback(answer), nil
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

// cleanFeedback reduces the model's answer to the advice lines. The prompt
// asks for lines starting with "- " or the sentinel, and the model does not
// always stop there: one answer opened with the sentinel and then explained
// why, another prefaced the lines with what it was about to do, and the
// launcher carried both into the agent's prompt as advice (SC-6039). So an
// answer whose first line is the sentinel is none whatever follows it, and of
// any other answer only the dashed lines count, at most feedbackMaxLines of
// them; prose around them is not advice. Code fences are stripped first.
func cleanFeedback(answer string) string {
	s := strings.TrimSpace(answer)
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if isFeedbackNone(lines[0]) {
		return ""
	}
	var advice []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") || len(advice) == feedbackMaxLines {
			continue
		}
		advice = append(advice, line)
	}
	return strings.Join(advice, "\n")
}

func isFeedbackNone(line string) bool {
	line = strings.TrimSuffix(strings.TrimSpace(line), ".")
	return strings.EqualFold(line, feedbackNone)
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

// launchAdvice asks the wired Feedback for this launch's block. The branch,
// which only the pull-request stages have, is read back off the dispatch line
// this package itself composed (prReviewDispatch and its siblings), so the
// three launch routes need no extra parameter to carry it.
func (d BoardTransitionDeps) launchAdvice(ctx context.Context, pmKey string, stage BoardStage, prompt string) string {
	if d.Feedback == nil {
		return ""
	}
	return d.Feedback(ctx, pmKey, stage, dispatchBranch(prompt))
}

// dispatchBranch returns the `--branch=` value on a prompt's dispatch line,
// "" when the line carries none.
func dispatchBranch(prompt string) string {
	line, _, _ := strings.Cut(prompt, "\n")
	for _, field := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(field, "--branch="); ok {
			return v
		}
	}
	return ""
}
