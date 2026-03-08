package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/middleware"
)

// compactParams is a minimal JSON Schema used for unexpanded tools to save tokens.
var compactParams = json.RawMessage(`{"type":"object","properties":{}}`)

// Registry holds registered tools and manages progressive disclosure state.
type Registry struct {
	tools         map[string]Tool
	expanded      map[string]bool
	hidden        map[string]bool // tools not included in ToolDefinitions at all
	defaultHidden map[string]bool // tools that should be re-hidden when stale
	lastUsed      map[string]int  // iteration when tool was last expanded/called
	order         []string        // insertion order for deterministic listing
	middlewares   []middleware.Middleware
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:         make(map[string]Tool),
		expanded:      make(map[string]bool),
		hidden:        make(map[string]bool),
		defaultHidden: make(map[string]bool),
		lastUsed:      make(map[string]int),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	name := t.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// RegisterAll adds multiple tools to the registry.
func (r *Registry) RegisterAll(tools ...Tool) {
	for _, t := range tools {
		r.Register(t)
	}
}

// Use adds a middleware to the execution chain.
// Middlewares are called in registration order, wrapping the tool's Execute method.
func (r *Registry) Use(mw middleware.Middleware) {
	r.middlewares = append(r.middlewares, mw)
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	return len(r.tools)
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// Execute runs a tool by name with raw JSON args, applying any registered middlewares.
func (r *Registry) Execute(
	ctx context.Context, name string, args string,
) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %q", name)
	}

	// Build the execution chain: middlewares wrap the final tool call.
	exec := t.Execute
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		mw := r.middlewares[i]
		next := exec
		exec = func(ctx context.Context, args string) (string, error) {
			return mw(ctx, name, args, next)
		}
	}
	return exec(ctx, args)
}

// Hide marks a tool as hidden so it is not included in ToolDefinitions output.
// Hidden tools are still callable and will be unhidden when detected in text
// via DetectToolHints or when explicitly expanded.
func (r *Registry) Hide(name string) {
	if _, ok := r.tools[name]; ok {
		r.hidden[name] = true
		r.defaultHidden[name] = true
	}
}

// UnhideHints detects hidden tools referenced in text and makes them visible
// (in compact mode, not expanded). Returns the names of newly-unhidden tools.
func (r *Registry) UnhideHints(text string) []string {
	hints := r.DetectToolHints(text)
	var unhidden []string
	for _, name := range hints {
		if r.hidden[name] {
			delete(r.hidden, name)
			unhidden = append(unhidden, name)
		}
	}
	return unhidden
}

// ToolDefinitions returns llm.ToolDefinition slice for passing to the LLM.
// Respects progressive disclosure: hidden tools are omitted, unexpanded tools
// get short descriptions and a minimal parameter schema to save tokens.
func (r *Registry) ToolDefinitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, name := range r.order {
		if r.hidden[name] {
			continue
		}
		t := r.tools[name]
		desc := t.Description()
		params := compactParams
		if r.expanded[name] {
			desc = t.FullDescription()
			params = t.Parameters()
		}
		defs = append(defs, llm.ToolDefinition{
			Name:        name,
			Description: desc,
			Parameters:  params,
		})
	}
	return defs
}

// Expand marks a tool for full description in subsequent ToolDefinitions() calls.
// Also unhides the tool if it was hidden.
func (r *Registry) Expand(name string) {
	if _, ok := r.tools[name]; ok {
		r.expanded[name] = true
		delete(r.hidden, name)
	}
}

// ExpandAt marks a tool as expanded, unhides it, and records the iteration
// for shrink tracking.
func (r *Registry) ExpandAt(name string, iter int) {
	if _, ok := r.tools[name]; ok {
		r.expanded[name] = true
		delete(r.hidden, name)
		r.lastUsed[name] = iter
	}
}

// ShrinkUnused collapses tools that haven't been called in the last
// staleAfter iterations, returning their schemas to compact mode.
// Tools expanded via Expand (without iteration tracking) are never shrunk.
// Tools originally registered as hidden are re-hidden when stale.
func (r *Registry) ShrinkUnused(currentIter, staleAfter int) {
	for name := range r.expanded {
		lastUsed, tracked := r.lastUsed[name]
		if !tracked {
			continue // expanded without iteration tracking — keep expanded
		}
		if currentIter-lastUsed > staleAfter {
			delete(r.expanded, name)
			delete(r.lastUsed, name)
			if r.defaultHidden[name] {
				r.hidden[name] = true
			}
		}
	}
}

// DetectToolHints scans text for references to registered tool names.
// Returns the names of tools that were mentioned but not yet expanded.
// Uses word-boundary matching so "file" doesn't false-match "read_file".
// For hidden tools, also matches individual name fragments (e.g., "web"
// matches "web_search") to enable keyword-based unhiding.
func (r *Registry) DetectToolHints(text string) []string {
	lower := strings.ToLower(text)
	var hints []string
	for name := range r.tools {
		if r.expanded[name] {
			continue
		}
		lowerName := strings.ToLower(name)
		// Match exact name or with underscores replaced by spaces.
		if containsWord(lower, lowerName) ||
			containsWord(lower, strings.ReplaceAll(lowerName, "_", " ")) {
			hints = append(hints, name)
			continue
		}
		// For hidden tools, also try matching individual name fragments
		// (e.g., "web" matches "web_search", "http" matches "http_request").
		if r.hidden[name] {
			for _, frag := range strings.Split(lowerName, "_") {
				if len(frag) >= 4 && containsWord(lower, frag) {
					hints = append(hints, name)
					break
				}
			}
		}
	}
	slices.Sort(hints)
	return hints
}

// containsWord checks if text contains word as a whole word
// (bounded by non-alphanumeric characters or string edges).
func containsWord(text, word string) bool {
	idx := 0
	for {
		pos := strings.Index(text[idx:], word)
		if pos < 0 {
			return false
		}
		pos += idx
		// Check left boundary.
		if pos > 0 && isWordChar(text[pos-1]) {
			idx = pos + len(word)
			continue
		}
		// Check right boundary.
		end := pos + len(word)
		if end < len(text) && isWordChar(text[end]) {
			idx = end
			continue
		}
		return true
	}
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// DiscoverSkills scans a directory for SKILL.md files and registers them.
// Pattern: <dir>/*/SKILL.md
func (r *Registry) DiscoverSkills(dir string) error {
	skills, err := discoverSkills(dir)
	if err != nil {
		return err
	}
	for _, s := range skills {
		r.Register(s)
	}
	return nil
}

// ReloadSkills re-discovers skills from the given directories and registers
// any new or changed skills. Returns the count of added/updated skills.
// Deleted skills are NOT removed to avoid breaking mid-conversation references.
func (r *Registry) ReloadSkills(dirs []string) int {
	updated := 0
	for _, dir := range dirs {
		skills, err := discoverSkills(dir)
		if err != nil {
			continue
		}
		for _, s := range skills {
			existing := r.tools[s.Name()]
			if existing != nil && existing.FullDescription() == s.FullDescription() {
				continue // unchanged
			}
			r.Register(s)
			updated++
		}
	}
	return updated
}

// List returns a formatted string listing all registered tools.
func (r *Registry) List() string {
	if len(r.order) == 0 {
		return "No tools registered."
	}

	var b strings.Builder
	var builtins, skills []string
	for _, name := range r.order {
		if strings.HasPrefix(name, "skill_") {
			skills = append(skills, name)
		} else {
			builtins = append(builtins, name)
		}
	}

	if len(builtins) > 0 {
		fmt.Fprintf(&b, "Built-in tools (%d):\n", len(builtins))
		for _, name := range builtins {
			fmt.Fprintf(&b, "  %-20s %s\n", name, r.tools[name].Description())
		}
	}
	if len(skills) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Skills (%d):\n", len(skills))
		for _, name := range skills {
			fmt.Fprintf(&b, "  %-20s %s\n", name, r.tools[name].Description())
		}
	}

	return b.String()
}
