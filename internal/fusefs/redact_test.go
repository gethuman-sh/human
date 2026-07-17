//go:build linux

package fusefs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactEnv_SensitiveKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"API_KEY", "API_KEY=abc123\n", "API_KEY=***\n"},
		{"SECRET_KEY", "SECRET_KEY=mysecret\n", "SECRET_KEY=***\n"},
		{"ACCESS_TOKEN", "ACCESS_TOKEN=tok_123\n", "ACCESS_TOKEN=***\n"},
		{"DB_PASSWORD", "DB_PASSWORD=hunter2\n", "DB_PASSWORD=***\n"},
		{"PASSWD", "DB_PASSWD=pass\n", "DB_PASSWD=***\n"},
		{"PWD suffix", "ADMIN_PWD=pass\n", "ADMIN_PWD=***\n"},
		{"CREDENTIAL", "AWS_CREDENTIAL=xyz\n", "AWS_CREDENTIAL=***\n"},
		{"AUTH", "OAUTH_TOKEN=xyz\n", "OAUTH_TOKEN=***\n"},
		{"AUTH_HEADER", "AUTH_HEADER=Bearer xyz\n", "AUTH_HEADER=***\n"},
		{"PRIVATE_KEY", "PRIVATE_KEY=-----BEGIN\n", "PRIVATE_KEY=***\n"},
		{"CERTIFICATE", "SSL_CERTIFICATE=abc\n", "SSL_CERTIFICATE=***\n"},
		{"CERT", "TLS_CERT=abc\n", "TLS_CERT=***\n"},
		{"case insensitive", "api_key=abc\n", "api_key=***\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(RedactEnv([]byte(tt.input)))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRedactEnv_SensitivePrefixes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"sk-", "OPENAI=sk-abc123def456\n", "OPENAI=***\n"},
		{"sk_live_", "STRIPE=sk_live_abc\n", "STRIPE=***\n"},
		{"sk_test_", "STRIPE_TEST=sk_test_abc\n", "STRIPE_TEST=***\n"},
		{"ghp_", "GITHUB=ghp_abc123\n", "GITHUB=***\n"},
		{"gho_", "GITHUB_OAUTH=gho_abc123\n", "GITHUB_OAUTH=***\n"},
		{"glpat-", "GITLAB=glpat-abc123\n", "GITLAB=***\n"},
		{"xoxb-", "SLACK_BOT=xoxb-abc123\n", "SLACK_BOT=***\n"},
		{"xoxp-", "SLACK_USER=xoxp-abc123\n", "SLACK_USER=***\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(RedactEnv([]byte(tt.input)))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRedactEnv_EmbeddedCredentials(t *testing.T) {
	input := "DATABASE_URL=postgres://admin:s3cret@db.host:5432/mydb\n"
	want := "DATABASE_URL=***\n"
	got := string(RedactEnv([]byte(input)))
	assert.Equal(t, want, got)
}

func TestRedactEnv_SafeKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"NODE_ENV", "NODE_ENV=production\n"},
		{"ENV", "ENV=staging\n"},
		{"ENVIRONMENT", "ENVIRONMENT=development\n"},
		{"DEBUG", "DEBUG=true\n"},
		{"LOG_LEVEL", "LOG_LEVEL=info\n"},
		{"PORT", "PORT=3000\n"},
		{"HOST", "HOST=localhost\n"},
		{"HOSTNAME", "HOSTNAME=myhost\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(RedactEnv([]byte(tt.input)))
			assert.Equal(t, tt.input, got, "safe key should be preserved as-is")
		})
	}
}

func TestRedactEnv_SafeValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"true", "SOME_FLAG=true\n"},
		{"false", "SOME_FLAG=false\n"},
		{"zero", "SOME_FLAG=0\n"},
		{"one", "SOME_FLAG=1\n"},
		{"development", "APP_MODE=development\n"},
		{"production", "APP_MODE=production\n"},
		{"staging", "APP_MODE=staging\n"},
		{"test", "APP_MODE=test\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(RedactEnv([]byte(tt.input)))
			assert.Equal(t, tt.input, got, "safe value should be preserved as-is")
		})
	}
}

func TestRedactEnv_CommentsAndBlanks(t *testing.T) {
	input := `# This is a comment
API_KEY=secret123

# Another comment
PORT=3000
`
	want := `# This is a comment
API_KEY=***

# Another comment
PORT=3000
`
	got := string(RedactEnv([]byte(input)))
	assert.Equal(t, want, got)
}

func TestRedactEnv_QuotedValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"double quoted safe", `NODE_ENV="production"` + "\n", `NODE_ENV="production"` + "\n"},
		{"single quoted safe", `NODE_ENV='production'` + "\n", `NODE_ENV='production'` + "\n"},
		{"double quoted secret", `API_KEY="sk-abc123"` + "\n", "API_KEY=***\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(RedactEnv([]byte(tt.input)))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRedactEnv_MixedContent(t *testing.T) {
	input := `# Database config
DATABASE_URL=postgres://user:pass@host:5432/db
DB_NAME=myapp

# App settings
NODE_ENV=production
PORT=3000
API_KEY=sk-1234567890
DEBUG=true
STRIPE_SECRET_KEY=sk_live_xxx
APP_MODE=staging
`
	want := `# Database config
DATABASE_URL=***
DB_NAME=myapp

# App settings
NODE_ENV=production
PORT=3000
API_KEY=***
DEBUG=true
STRIPE_SECRET_KEY=***
APP_MODE=staging
`
	got := string(RedactEnv([]byte(input)))
	assert.Equal(t, want, got)
}

func TestRedactEnv_NoTrailingNewline(t *testing.T) {
	input := "API_KEY=secret"
	want := "API_KEY=***"
	got := string(RedactEnv([]byte(input)))
	assert.Equal(t, want, got)
}

func TestRedactEnv_EmptyInput(t *testing.T) {
	got := string(RedactEnv([]byte("")))
	assert.Equal(t, "", got)
}

func TestRedactEnv_URLWithoutCredentials(t *testing.T) {
	input := "HOMEPAGE=https://example.com/path\n"
	got := string(RedactEnv([]byte(input)))
	assert.Equal(t, input, got, "URL without credentials should not be redacted")
}

// A single line larger than bufio.MaxScanTokenSize (64 KiB) — realistic for a
// base64-encoded certificate or long JWT chain — must still be redacted and
// followed by later lines, not silently dropped mid-file.
func TestRedactEnv_LongLineDoesNotTruncateFollowingLines(t *testing.T) {
	longValue := make([]byte, 128*1024)
	for i := range longValue {
		longValue[i] = 'a'
	}
	input := "TLS_CERT=" + string(longValue) + "\nPORT=3000\n"
	got := string(RedactEnv([]byte(input)))
	assert.Equal(t, "TLS_CERT=***\nPORT=3000\n", got)
}
