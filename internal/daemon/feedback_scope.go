package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// feedbackScope is what one launch is about: the ticket's title for the
// briefing's first line and the files the stage is likely to touch, which
// pick the record's file-scoped rows.
type feedbackScope struct {
	title string
	files []string
}

// feedbackScopeFiles caps the file set a scope names; past it the record's
// file layer would be the whole record and the project-wide layer already
// covers that.
const feedbackScopeFiles = 30

// scope derives the files per stage from artifacts every project has —
// planning stages from the ticket text, implementation stages from the plan
// comment, the pull-request stages from the branch's diff. A scope that
// cannot be computed is empty rather than an error: the block then rests on
// the project-wide layers alone, which is still advice.
func (f *FeedbackDeps) scope(ctx context.Context, key string, stage BoardStage, branch string) feedbackScope {
	title, body := f.ticketText(ctx, key)
	s := feedbackScope{title: title}
	switch stage {
	case prReviewAgentStage, prFixAgentStage, deployFixAgentStage:
		s.files = f.branchFiles(ctx, key, branch)
	case BoardImplementation, BoardVerification:
		if plan, ok := f.planBody(ctx, key); ok {
			s.files = pathTokens(plan, f.Workspace)
		}
		if len(s.files) == 0 {
			s.files = pathTokens(title+"\n"+body, f.Workspace)
		}
	default:
		s.files = pathTokens(title+"\n"+body, f.Workspace)
	}
	return s
}

func (f *FeedbackDeps) ticketText(ctx context.Context, key string) (title, body string) {
	if f.Ticket == nil {
		return "", ""
	}
	issue, err := f.Ticket.GetIssue(ctx, key)
	if err != nil || issue == nil {
		f.Logger.Debug().Err(err).Str("pm", key).Msg("launch advice: ticket unreadable; scope falls back to the record alone")
		return "", ""
	}
	return strings.TrimSpace(issue.Title), issue.Description
}

func (f *FeedbackDeps) planBody(ctx context.Context, key string) (string, bool) {
	if f.Comments == nil {
		return "", false
	}
	comments, err := f.Comments.ListComments(ctx, key)
	if err != nil {
		f.Logger.Debug().Err(err).Str("pm", key).Msg("launch advice: thread unreadable; scope falls back to the ticket text")
		return "", false
	}
	return latestPlanComment(comments)
}

func (f *FeedbackDeps) branchFiles(ctx context.Context, key, branch string) []string {
	if branch == "" || f.Workspace == "" {
		return nil
	}
	changed := f.ChangedFiles
	if changed == nil {
		changed = gitChangedFiles
	}
	files, err := changed(ctx, f.Workspace, branch)
	if err != nil {
		f.Logger.Debug().Err(err).Str("pm", key).Str("branch", branch).
			Msg("launch advice: branch diff unavailable; scope falls back to the record alone")
		return nil
	}
	if len(files) > feedbackScopeFiles {
		files = files[:feedbackScopeFiles]
	}
	return files
}

// pathTokenPattern matches what a ticket or plan writes when it names a file:
// a slash-joined path or a bare filename with an extension, possibly wrapped
// in backticks or followed by punctuation the surrounding prose adds.
var pathTokenPattern = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.\-]*(?:/[A-Za-z0-9_.\-]+)+|[A-Za-z0-9_][A-Za-z0-9_\-]*\.[A-Za-z]{1,6}\b`)

// pathTokens returns the distinct tokens of text that exist as files under
// workspace, in first-mention order. Existence is the filter that turns a
// regex over prose into a file list: a version number, a domain or a Go
// package path is a token too, and none of those is a file.
func pathTokens(text, workspace string) []string {
	if workspace == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range pathTokenPattern.FindAllString(text, -1) {
		tok = strings.TrimRight(tok, ".,:;)")
		tok = strings.TrimPrefix(tok, "./")
		if tok == "" || seen[tok] || strings.Contains(tok, "..") {
			continue
		}
		seen[tok] = true
		if !isWorkspaceFile(workspace, tok) {
			continue
		}
		out = append(out, tok)
		if len(out) >= feedbackScopeFiles {
			break
		}
	}
	return out
}

func isWorkspaceFile(workspace, rel string) bool {
	full := filepath.Join(workspace, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, filepath.Clean(workspace)+string(filepath.Separator)) {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.Mode().IsRegular()
}

// gitChangedFiles lists the files branch changes against the remote's default
// branch, from the workspace's own remote-tracking refs after one fetch. It
// reads only: the workspace is the shared checkout the agents run in, and a
// fetch adds refs without touching the tree or the index.
func gitChangedFiles(ctx context.Context, workspace, branch string) ([]string, error) {
	if _, err := git(ctx, workspace, "fetch", "--quiet", "origin"); err != nil {
		return nil, err
	}
	base := "origin/main"
	if head, err := git(ctx, workspace, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(head) != "" {
		base = strings.TrimSpace(head)
	}
	out, err := git(ctx, workspace, "diff", "--name-only", base+"...origin/"+branch)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary; args are this file's literals plus a branch name the daemon itself composed
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
