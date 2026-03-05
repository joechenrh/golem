package redact

import (
	"testing"
)

func TestRedact_OpenAIKey(t *testing.T) {
	r := New()
	input := "key is sk-proj-abc123def456ghi789jkl"
	got := r.Redact(input)
	want := "key is [REDACTED:api_key]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_AnthropicKey(t *testing.T) {
	r := New()
	input := "key: sk-ant-api03-abcdefghij1234567890klmno"
	got := r.Redact(input)
	want := "key: [REDACTED:anthropic_key]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_AWSKey(t *testing.T) {
	r := New()
	input := "aws_access_key_id = AKIAIOSFODNN7EXAMPLE"
	got := r.Redact(input)
	want := "aws_access_key_id = [REDACTED:aws_key]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_BearerToken(t *testing.T) {
	r := New()
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc"
	got := r.Redact(input)
	want := "Authorization: [REDACTED:bearer_token]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_PrivateKey(t *testing.T) {
	r := New()
	input := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQ..."
	got := r.Redact(input)
	want := "[REDACTED:private_key]\nMIIEpAIBAAKCAQ..."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_EnvAssignment(t *testing.T) {
	r := New()
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "OPENAI_API_KEY=sk-proj-abc123def456ghi789jkl",
			want:  "OPENAI_API_KEY=[REDACTED:env_secret]",
		},
		{
			input: "password: s3cret123",
			want:  "password: [REDACTED:env_secret]",
		},
		{
			input: "export SECRET=mysecretvalue",
			want:  "export SECRET=[REDACTED:env_secret]",
		},
		{
			input: "DB_TOKEN = tok_abc123",
			want:  "DB_TOKEN = [REDACTED:env_secret]",
		},
	}
	for _, tt := range tests {
		got := r.Redact(tt.input)
		if got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRedact_URLCredentials(t *testing.T) {
	r := New()
	input := "postgres://admin:s3cret@db.example.com:5432/mydb"
	got := r.Redact(input)
	want := "postgres[REDACTED:url_credentials]db.example.com:5432/mydb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_MixedContent(t *testing.T) {
	r := New()
	input := `Config loaded:
  OPENAI_API_KEY=sk-proj-abc123def456ghi789jkl
  DB_URL=postgres://user:pass@localhost:5432/db
  DEBUG=true`
	got := r.Redact(input)
	want := `Config loaded:
  OPENAI_API_KEY=[REDACTED:env_secret]
  DB_URL=postgres[REDACTED:url_credentials]localhost:5432/db
  DEBUG=true`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRedact_NoFalsePositives(t *testing.T) {
	r := New()
	safe := []string{
		"func main() { fmt.Println(\"hello\") }",
		"The quick brown fox jumps over the lazy dog",
		"sk-short",                         // too short for api_key pattern
		"Bearer short",                     // too short for bearer pattern
		"https://example.com/api/v1/users", // URL without credentials
		"AKIA1234",                         // too short for AWS key
		"import \"crypto/rand\"",
		"return nil, fmt.Errorf(\"token expired\")",
		"x := map[string]string{}",
	}
	for _, s := range safe {
		got := r.Redact(s)
		if got != s {
			t.Errorf("false positive: Redact(%q) = %q", s, got)
		}
	}
}

func TestRedact_Idempotent(t *testing.T) {
	r := New()
	input := "key: sk-proj-abc123def456ghi789jkl"
	first := r.Redact(input)
	second := r.Redact(first)
	if first != second {
		t.Errorf("not idempotent: first=%q, second=%q", first, second)
	}
}
