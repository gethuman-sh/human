package audit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectMutating(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantOK  bool
		wantOp  MutatingOp
	}{
		{
			name:   "create",
			args:   []string{"jira", "issue", "create", "--project=KAN", "Title"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "create", TrackerKind: "jira", Project: "KAN"},
		},
		{
			name:   "createSpaceFlag",
			args:   []string{"jira", "issue", "create", "--project", "KAN", "Title"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "create", TrackerKind: "jira", Project: "KAN"},
		},
		{
			name:   "edit",
			args:   []string{"linear", "issue", "edit", "HUM-1", "--title", "x"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "edit", TrackerKind: "linear", Key: "HUM-1"},
		},
		{
			name:   "delete",
			args:   []string{"jira", "issue", "delete", "KAN-2"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "delete", TrackerKind: "jira", Key: "KAN-2"},
		},
		{
			name:   "commentAdd",
			args:   []string{"jira", "issue", "comment", "add", "KAN-3", "hi"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "comment", TrackerKind: "jira", Key: "KAN-3"},
		},
		{
			name:   "commentList",
			args:   []string{"jira", "issue", "comment", "list", "KAN-3"},
			wantOK: false,
		},
		{
			name:   "status",
			args:   []string{"jira", "issue", "status", "KAN-4", "Done"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "status", TrackerKind: "jira", Key: "KAN-4"},
		},
		{
			name:   "statuses",
			args:   []string{"jira", "issue", "statuses", "KAN-4"},
			wantOK: false,
		},
		{
			name:   "start",
			args:   []string{"jira", "issue", "start", "KAN-5"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "start", TrackerKind: "jira", Key: "KAN-5"},
		},
		{
			name:   "trackerName",
			args:   []string{"jira", "--tracker", "work", "issue", "delete", "KAN-6"},
			wantOK: true,
			wantOp: MutatingOp{Operation: "delete", TrackerKind: "jira", TrackerName: "work", Key: "KAN-6"},
		},
		{
			name:   "readOnly",
			args:   []string{"jira", "issue", "get", "KAN-7"},
			wantOK: false,
		},
		{
			name:   "nonTracker",
			args:   []string{"audit", "list"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op, ok := DetectMutating(tt.args)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantOp, op)
			}
		})
	}
}
