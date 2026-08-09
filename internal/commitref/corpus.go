package commitref

// Case is one commit message and the verdict the grammar must reach on it.
type Case struct {
	// Message is the whole commit message, as git would hand it to the hook.
	Message string
	// Accepted is whether it may be committed: it carries a reference, or its
	// kind excuses it from needing one.
	Accepted bool
	// Why names what the case is testing, so a failure says which rule moved.
	Why string
}

// Corpus is the shared truth about the grammar.
//
// Two things implement it — this package and .githooks/commit-msg — and neither
// can be derived from the other: the hook must stay shell, because a hook that
// needs the binary built cannot be the thing that gates building it. So they are
// held in step by both being run over these cases, which is what turns "two
// spellings of one rule" from a silent drift into a failing test.
//
// It lives in the package rather than in a _test.go file because the hook's own
// check needs it too, from a different package.
var Corpus = []Case{
	// Every accepted form, as the rejection message advertises it. A form
	// advertised and not accepted is the cruellest version of this bug.
	{Message: "Fix the parser\n\nIssue #123", Accepted: true, Why: "numeric, as advertised"},
	{Message: "Fix the parser\n\nIssue HUM-30", Accepted: true, Why: "prefixed, as advertised"},
	{Message: "[SC-57] Fix the parser", Accepted: true, Why: "bracket, as advertised"},
	{Message: "Fix the parser\n\noctocat/repo#42", Accepted: true, Why: "code host, as advertised"},
	{Message: "Fix the parser\n\nMyProject/42", Accepted: true, Why: "project path, as advertised"},

	// The real shapes this project commits in.
	{Message: "[SC-79] [HUM-59] Add validation", Accepted: true, Why: "split topology names both keys"},
	{Message: "[SC-4118] Give settings.json one editor", Accepted: true, Why: "the ordinary single-tracker commit"},

	// Kinds that carry no reference of their own because they inherit one.
	{Message: "Merge pull request #460 from gethuman-sh/fix/x", Accepted: true, Why: "a merge is exempt"},
	{Message: "Revert \"[SC-1] Add validation\"", Accepted: true, Why: "a revert is exempt"},
	{Message: "fixup! [SC-1] Add validation", Accepted: true, Why: "a fixup is exempt"},
	{Message: "squash! [SC-1] Add validation", Accepted: true, Why: "a squash is exempt"},

	// Rejected: no reference anywhere.
	{Message: "Fix the parser", Accepted: false, Why: "a subject alone is not a reference"},
	{Message: "Fix the parser\n\nIt was wrong.", Accepted: false, Why: "prose is not a reference"},
	{Message: "Update SC docs", Accepted: false, Why: "a prefix without a number is not a key"},
	{Message: "Bump to 1.2.3", Accepted: false, Why: "a version is not a reference"},
	{Message: "Merged the two paths", Accepted: false, Why: "only a real merge subject is exempt, not a word"},
}
