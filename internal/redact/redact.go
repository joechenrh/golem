package redact

import (
	"regexp"
	"strings"
)

// pattern pairs a compiled regex with a human-readable category name.
type pattern struct {
	name string
	re   *regexp.Regexp
	tmpl string // replacement template; if empty, uses "[REDACTED:<name>]"
}

// Redactor applies secret-detection patterns to text and masks matches.
type Redactor struct {
	patterns []pattern
}

// New builds a Redactor with the default set of secret patterns.
func New() *Redactor {
	return &Redactor{
		patterns: defaultPatterns(),
	}
}

// Redact returns s with all detected secrets replaced by [REDACTED:<category>].
func (r *Redactor) Redact(s string) string {
	for _, p := range r.patterns {
		tmpl := p.tmpl
		if tmpl == "" {
			tmpl = "[REDACTED:" + p.name + "]"
		}
		s = p.re.ReplaceAllString(s, tmpl)
	}
	return s
}

var envSecretKeywords = strings.Join([]string{
	"api_key", "apikey", "password", "passwd",
	"secret", "token", "private_key",
}, "|")

// defaultPatterns returns the built-in secret detection patterns.
// Order matters: more specific patterns come before generic ones.
func defaultPatterns() []pattern {
	return []pattern{
		{
			name: "private_key",
			re:   regexp.MustCompile(`-----BEGIN [A-Z ]+ KEY-----`),
		},
		{
			name: "anthropic_key",
			re:   regexp.MustCompile(`sk-ant-[a-zA-Z0-9\-]{20,}`),
		},
		{
			name: "api_key",
			re:   regexp.MustCompile(`sk-[a-zA-Z0-9\-]{20,}`),
		},
		{
			name: "aws_key",
			re:   regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		},
		{
			name: "bearer_token",
			re:   regexp.MustCompile(`Bearer [a-zA-Z0-9\-_.]{20,}`),
		},
		{
			name: "url_credentials",
			re:   regexp.MustCompile(`://[^:@/\s]+:[^@/\s]+@`),
		},
		{
			// Matches lines like API_KEY=value or token: value.
			// Preserves the key name and separator, redacts only the value.
			name: "env_secret",
			re:   regexp.MustCompile(`(?i)((?:` + envSecretKeywords + `)\s*[=:]\s*)(\S+)`),
			tmpl: "${1}[REDACTED:env_secret]",
		},
	}
}
