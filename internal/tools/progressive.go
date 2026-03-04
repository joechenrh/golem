package tools

// ExpandHints expands all tools detected in the text.
// Convenience wrapper around DetectToolHints + Expand.
func (r *Registry) ExpandHints(text string) []string {
	hints := r.DetectToolHints(text)
	for _, name := range hints {
		r.Expand(name)
	}
	return hints
}
