package tools

import "context"

// BackgroundTaskTracker allows tools to launch and manage background tasks
// without importing the agent package (avoids circular deps).
type BackgroundTaskTracker interface {
	// Launch starts a background task via errgroup. The callback receives
	// the task's context and ID. Returns the task ID immediately.
	Launch(desc string, fn func(ctx context.Context, id int)) int
	// Complete marks a task as successfully completed with a result.
	Complete(id int, result string)
	// Fail marks a task as failed with an error message.
	Fail(id int, errMsg string)
	// SetChildTracker attaches a sub-agent's tracker to a task for tree display.
	SetChildTracker(id int, child BackgroundTaskTracker)
	// TreeSummary returns a recursive, indented summary of all tasks.
	TreeSummary(indent string) string
}

type taskTrackerKey struct{}

// WithTaskTracker returns a context carrying the given tracker.
func WithTaskTracker(ctx context.Context, tt BackgroundTaskTracker) context.Context {
	return context.WithValue(ctx, taskTrackerKey{}, tt)
}

// GetTaskTracker retrieves the tracker from context, or nil if none.
func GetTaskTracker(ctx context.Context) BackgroundTaskTracker {
	tt, _ := ctx.Value(taskTrackerKey{}).(BackgroundTaskTracker)
	return tt
}
