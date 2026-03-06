package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// SafetyHook blocks dangerous tool calls before execution.
// Covers shell command safety, SSRF protection, and sensitive file writes.
type SafetyHook struct{}

// NewSafetyHook creates a SafetyHook.
func NewSafetyHook() *SafetyHook {
	return &SafetyHook{}
}

func (h *SafetyHook) Name() string { return "safety" }

func (h *SafetyHook) Handle(_ context.Context, event Event) error {
	if event.Type != EventBeforeToolExec {
		return nil
	}

	toolName, _ := event.Payload["tool_name"].(string)
	args, _ := event.Payload["arguments"].(string)

	switch toolName {
	case "shell_exec":
		return h.checkShell(args)
	case "web_fetch", "http_request":
		return h.checkWebFetch(args)
	case "write_file", "edit_file":
		return h.checkFileWrite(args)
	}
	return nil
}

// ── Shell safety ─────────────────────────────────────────────────

// dangerousPatterns matches shell commands that are destructive,
// exfiltrating, or otherwise unsafe for an autonomous agent.
var dangerousPatterns = []*regexp.Regexp{
	// Destructive filesystem operations.
	regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rR]|-[a-zA-Z]*f)[a-zA-Z]*\s+/`), // rm -rf / or rm -r /
	regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rR]|-[a-zA-Z]*f)[a-zA-Z]*\s+~`), // rm -rf ~
	regexp.MustCompile(`\bmkfs\b`),                                         // format filesystem
	regexp.MustCompile(`\bdd\b.*\bof=/dev/`),                               // raw disk write
	regexp.MustCompile(`>\s*/dev/sd[a-z]`),                                 // redirect to block device
	regexp.MustCompile(`:\(\)\s*\{\s*:\|\s*:\s*&\s*\}`),                    // fork bomb
	regexp.MustCompile(`\bchmod\s+(-[a-zA-Z]*\s+)*777\s+/`),                // chmod 777 on root paths
	regexp.MustCompile(`\bchown\s+(-[a-zA-Z]*\s+)*root\b.*\s+/`),           // chown root on system paths

	// Exfiltration / remote code execution.
	regexp.MustCompile(`\bcurl\b.*\|\s*(ba)?sh`),                                // curl | sh
	regexp.MustCompile(`\bwget\b.*\|\s*(ba)?sh`),                                // wget | sh
	regexp.MustCompile(`\bcurl\b.*\|\s*python`),                                 // curl | python
	regexp.MustCompile(`\bwget\b.*-O\s*-\s*\|\s*(ba)?sh`),                       // wget -O - | sh
	regexp.MustCompile(`\beval\s*\$\(curl`),                                     // eval $(curl ...)
	regexp.MustCompile(`\bnc\s+(-[a-zA-Z]*\s+)*[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`), // netcat to IP

	// Shutdown / reboot.
	regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff|init\s+[06])\b`),

	// History / credential theft.
	regexp.MustCompile(`\bcat\b.*/etc/(shadow|passwd|sudoers)\b`),
	regexp.MustCompile(`\bcat\b.*\.(ssh|gnupg|aws|kube)/`),
}

func (h *SafetyHook) checkShell(args string) error {
	var params struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &params) != nil || params.Command == "" {
		return nil
	}

	for _, pat := range dangerousPatterns {
		if pat.MatchString(params.Command) {
			return fmt.Errorf("blocked dangerous shell command: %s", truncateStr(params.Command, 100))
		}
	}
	return nil
}

// ── SSRF protection ──────────────────────────────────────────────

// privateNetworks defines CIDR ranges for private/reserved IPs.
var privateNetworks = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16", // link-local + AWS metadata
		"0.0.0.0/8",
		"::1/128",
		"fc00::/7",  // IPv6 ULA
		"fe80::/10", // IPv6 link-local
	}
	var nets []*net.IPNet
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		nets = append(nets, n)
	}
	return nets
}()

func (h *SafetyHook) checkWebFetch(args string) error {
	var params struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(args), &params) != nil || params.URL == "" {
		return nil
	}

	parsed, err := url.Parse(params.URL)
	if err != nil {
		return nil // let the tool handle invalid URLs
	}

	hostname := parsed.Hostname()

	// Block cloud metadata endpoints.
	if hostname == "metadata.google.internal" || hostname == "metadata" {
		return fmt.Errorf("blocked request to cloud metadata endpoint: %s", hostname)
	}

	// Resolve and check IP.
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return nil // DNS failure will be handled by the tool
	}
	for _, ip := range ips {
		for _, pn := range privateNetworks {
			if pn.Contains(ip) {
				return fmt.Errorf("blocked request to private/reserved IP: %s (%s)", hostname, ip)
			}
		}
	}
	return nil
}

// ── Sensitive file write protection ──────────────────────────────

var sensitivePathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|/)\.env($|\.)`),           // .env, .env.local, .env.production
	regexp.MustCompile(`(^|/)\.git/config$`),         // git config
	regexp.MustCompile(`(^|/)id_rsa`),                // SSH keys
	regexp.MustCompile(`(^|/)id_ed25519`),            // SSH keys
	regexp.MustCompile(`(^|/)\.ssh/`),                // SSH directory
	regexp.MustCompile(`(^|/)credentials(\.json)?$`), // credential files
	regexp.MustCompile(`(^|/)\.aws/`),                // AWS config
	regexp.MustCompile(`(^|/)\.kube/`),               // Kubernetes config
	regexp.MustCompile(`(^|/)\.gnupg/`),              // GPG keys
	regexp.MustCompile(`(^|/)authorized_keys$`),      // SSH authorized keys
}

func (h *SafetyHook) checkFileWrite(args string) error {
	var params struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &params) != nil || params.Path == "" {
		return nil
	}

	lower := strings.ToLower(params.Path)
	for _, pat := range sensitivePathPatterns {
		if pat.MatchString(lower) {
			return fmt.Errorf("blocked write to sensitive path: %s", params.Path)
		}
	}
	return nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
