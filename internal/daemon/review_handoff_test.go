package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseEngineeringKeysFromHandoff(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "happy path single key",
			body: "[human:ready-for-review]\nengineering: HUM-89\nbranch: main\ncommits: 2037e40",
			want: []string{"HUM-89"},
		},
		{
			name: "multiple keys with whitespace",
			body: "[human:ready-for-review]\nengineering: HUM-89,  HUM-90, HUM-91 \nbranch: main",
			want: []string{"HUM-89", "HUM-90", "HUM-91"},
		},
		{
			name: "body must start with header so quoted references don't trigger",
			body: "> [human:ready-for-review]\n> engineering: HUM-89",
			want: nil,
		},
		{
			name: "header present but engineering line missing",
			body: "[human:ready-for-review]\nbranch: main",
			want: nil,
		},
		{
			name: "not a handoff",
			body: "Looks good to me!",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEngineeringKeysFromHandoff(tt.body)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseCommitsFromHandoff(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "happy path multiple commits with whitespace",
			body: "[human:ready-for-review]\nengineering: HUM-89\nbranch: main\ncommits: abc123,  def456 , 789abc",
			want: []string{"abc123", "def456", "789abc"},
		},
		{
			name: "single commit",
			body: "[human:ready-for-review]\nbranch: main\ncommits: 2037e40",
			want: []string{"2037e40"},
		},
		{
			name: "body must start with header so quoted references don't trigger",
			body: "> [human:ready-for-review]\n> commits: abc123",
			want: nil,
		},
		{
			name: "header present but commits line missing",
			body: "[human:ready-for-review]\nbranch: main",
			want: nil,
		},
		{
			name: "not a handoff",
			body: "Looks good to me!",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommitsFromHandoff(tt.body)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParsePRFromHandoff(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "handoff with pr line",
			body: "[human:ready-for-review]\nengineering: HUM-89\nbranch: autofix/hum-89\ncommits: 2037e40\npr: https://github.com/o/r/pull/7",
			want: "https://github.com/o/r/pull/7",
		},
		{
			name: "handoff without pr line",
			body: "[human:ready-for-review]\nengineering: HUM-89\nbranch: main",
			want: "",
		},
		{
			name: "not a handoff",
			body: "pr: https://example.com/pull/1",
			want: "",
		},
		{
			name: "quoted header does not trigger",
			body: "> [human:ready-for-review]\n> pr: https://github.com/o/r/pull/7",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParsePRFromHandoff(tt.body))
		})
	}
}

func TestIsReviewComplete(t *testing.T) {
	assert.True(t, IsReviewComplete("[human:review-complete]\nverdict: pass"))
	assert.True(t, IsReviewComplete("  [human:review-complete]\nverdict: pass"))
	assert.False(t, IsReviewComplete("[human:ready-for-review]"))
	assert.False(t, IsReviewComplete("plain comment"))
}
