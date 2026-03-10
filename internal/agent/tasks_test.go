package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskTracker_AddAndComplete(t *testing.T) {
	tt := NewTaskTracker(5)

	_, cancel1 := context.WithCancel(context.Background())
	_, cancel2 := context.WithCancel(context.Background())

	id1 := tt.Add("task one", cancel1)
	id2 := tt.Add("task two", cancel2)

	if id1 != 1 || id2 != 2 {
		t.Fatalf("IDs = %d, %d; want 1, 2", id1, id2)
	}

	tt.Complete(id1, "result one")

	tt.mu.Lock()
	task1 := tt.tasks[id1]
	task2 := tt.tasks[id2]
	tt.mu.Unlock()

	if task1.Status != TaskCompleted {
		t.Errorf("task1.Status = %v, want completed", task1.Status)
	}
	if task1.Result != "result one" {
		t.Errorf("task1.Result = %q, want %q", task1.Result, "result one")
	}
	if task2.Status != TaskRunning {
		t.Errorf("task2.Status = %v, want running", task2.Status)
	}
}

func TestTaskTracker_Fail(t *testing.T) {
	tt := NewTaskTracker(5)
	_, cancel := context.WithCancel(context.Background())
	id := tt.Add("failing task", cancel)

	tt.Fail(id, "something broke")

	tt.mu.Lock()
	task := tt.tasks[id]
	tt.mu.Unlock()

	if task.Status != TaskFailed {
		t.Errorf("Status = %v, want failed", task.Status)
	}
	if task.Error != "something broke" {
		t.Errorf("Error = %q, want %q", task.Error, "something broke")
	}
	if task.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set")
	}
}

func TestTaskTracker_DrainCompleted(t *testing.T) {
	tt := NewTaskTracker(5)
	_, c1 := context.WithCancel(context.Background())
	_, c2 := context.WithCancel(context.Background())
	_, c3 := context.WithCancel(context.Background())

	id1 := tt.Add("a", c1)
	tt.Add("b", c2)
	id3 := tt.Add("c", c3)

	tt.Complete(id1, "done")
	tt.Fail(id3, "err")

	drained := tt.DrainCompleted()
	if len(drained) != 2 {
		t.Fatalf("DrainCompleted() returned %d tasks, want 2", len(drained))
	}

	// Second drain should return nothing (already injected).
	drained2 := tt.DrainCompleted()
	if len(drained2) != 0 {
		t.Fatalf("second DrainCompleted() returned %d tasks, want 0", len(drained2))
	}
}

func TestTaskTracker_CancelAll(t *testing.T) {
	tt := NewTaskTracker(5)

	var cancelled1, cancelled2 atomic.Bool
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())

	go func() {
		<-ctx1.Done()
		cancelled1.Store(true)
	}()
	go func() {
		<-ctx2.Done()
		cancelled2.Store(true)
	}()

	tt.Add("a", cancel1)
	tt.Add("b", cancel2)

	tt.CancelAll()

	// Give goroutines a moment to observe cancellation.
	time.Sleep(10 * time.Millisecond)

	if !cancelled1.Load() || !cancelled2.Load() {
		t.Error("CancelAll should cancel all running tasks")
	}
}

func TestTaskTracker_Close(t *testing.T) {
	tt := NewTaskTracker(5)

	var done atomic.Bool
	tt.g.Go(func() error {
		time.Sleep(50 * time.Millisecond)
		done.Store(true)
		return nil
	})

	tt.Close()

	if !done.Load() {
		t.Error("Close should wait for goroutines to finish")
	}
}

func TestTaskTracker_Launch(t *testing.T) {
	tt := NewTaskTracker(5)

	id := tt.Launch("test task", func(ctx context.Context, taskID int) {
		tt.Complete(taskID, "launched result")
	})

	if id != 1 {
		t.Fatalf("Launch returned ID %d, want 1", id)
	}

	// Wait for the goroutine to finish.
	tt.g.Wait()

	tt.mu.Lock()
	task := tt.tasks[id]
	tt.mu.Unlock()

	if task.Status != TaskCompleted {
		t.Errorf("Status = %v, want completed", task.Status)
	}
	if task.Result != "launched result" {
		t.Errorf("Result = %q, want %q", task.Result, "launched result")
	}
}

func TestTaskTracker_ConcurrencyLimit(t *testing.T) {
	tt := NewTaskTracker(2)

	var running atomic.Int32
	var maxRunning atomic.Int32
	var wg sync.WaitGroup

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tt.Launch("task", func(ctx context.Context, id int) {
				cur := running.Add(1)
				// Track maximum concurrency.
				for {
					old := maxRunning.Load()
					if cur <= old || maxRunning.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				running.Add(-1)
				tt.Complete(id, "ok")
			})
		}()
	}

	wg.Wait()
	tt.g.Wait()

	if maxRunning.Load() > 2 {
		t.Errorf("maxRunning = %d, want <= 2", maxRunning.Load())
	}
}

func TestTaskTracker_Summary(t *testing.T) {
	tt := NewTaskTracker(5)
	_, c1 := context.WithCancel(context.Background())
	_, c2 := context.WithCancel(context.Background())
	_, c3 := context.WithCancel(context.Background())

	tt.Add("fix bug A", c1)
	id2 := tt.Add("fix bug B", c2)
	id3 := tt.Add("fix bug C", c3)

	tt.Complete(id2, "fixed")
	tt.Fail(id3, "timeout")

	summary := tt.Summary()

	if !strings.Contains(summary, "[running]") {
		t.Error("summary should contain [running]")
	}
	if !strings.Contains(summary, "[completed]") {
		t.Error("summary should contain [completed]")
	}
	if !strings.Contains(summary, "[failed]") {
		t.Error("summary should contain [failed]")
	}
	if !strings.Contains(summary, "fix bug A") {
		t.Error("summary should contain task description")
	}
}

func TestTaskTracker_SummaryEmpty(t *testing.T) {
	tt := NewTaskTracker(5)
	summary := tt.Summary()
	if summary != "No background tasks." {
		t.Errorf("Summary() = %q, want %q", summary, "No background tasks.")
	}
}
