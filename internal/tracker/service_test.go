package tracker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/store"
)

func testTracker(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tr, err := New(st.DB(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestCreateTransitionClaimAndLease(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	task, err := tr.Create(ctx, CreateInput{
		Title: "fix it", Description: "desc", Context: "ctx",
		Complexity: 3, Priority: 4, Status: StatusReady, CreatedBy: "user",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" || task.ProjectID == "" {
		t.Fatalf("task = %#v", task)
	}
	if task.Status != StatusReady {
		t.Fatalf("status = %s", task.Status)
	}
	claimed, err := tr.ClaimNext(ctx, "coder", []WorkerSlot{{ID: "coder", Complexities: []int{3}}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != task.ID || claimed.Status != StatusClaimed {
		t.Fatalf("claimed = %#v", claimed)
	}
	if _, err := tr.ClaimNext(ctx, "coder2", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Transition(ctx, task.ID, StatusInProgress, "coder"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Transition(ctx, task.ID, StatusReview, "coder"); err != nil {
		t.Fatal(err)
	}
	done, err := tr.Transition(ctx, task.ID, StatusDone, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusDone {
		t.Fatalf("status = %s", done.Status)
	}
}

func TestClaimRespectsComplexityAndDependencies(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	a, _ := tr.Create(ctx, CreateInput{Title: "a", Complexity: 5, Priority: 5, Status: StatusReady, CreatedBy: "user"}, "user")
	b, _ := tr.Create(ctx, CreateInput{Title: "b", Complexity: 3, Priority: 3, Status: StatusReady, DependsOn: []string{a.ID}, CreatedBy: "user"}, "user")
	got, err := tr.ClaimNext(ctx, "cheap", []WorkerSlot{{ID: "cheap", Complexities: []int{1, 2}}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("claimed ineligible task %s", got.ID)
	}
	got, err = tr.ClaimNext(ctx, "mid", []WorkerSlot{{ID: "mid", Complexities: []int{3}}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("claimed dependency task %s", got.ID)
	}
	if _, err := tr.AddComment(ctx, b.ID, "master", "note"); err != nil {
		t.Fatal(err)
	}
	comments, _ := tr.Comments(ctx, b.ID)
	if len(comments) != 1 {
		t.Fatalf("comments = %d", len(comments))
	}
}

func TestRecoverInterruptedTask(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	task, _ := tr.Create(ctx, CreateInput{Title: "recover", Complexity: 1, Priority: 3, Status: StatusReady, CreatedBy: "user"}, "user")
	claimed, _ := tr.ClaimNext(ctx, "coder", []WorkerSlot{{ID: "coder", Complexities: []int{1}}}, time.Minute)
	if claimed == nil {
		t.Fatal("task was not claimed")
	}
	_ = tr.StartTask(ctx, task.ID, "coder")
	attempt, err := tr.StartAttempt(ctx, task.ID, claimed.Attempts, "agent", "coder", "mock")
	if err != nil {
		t.Fatal(err)
	}
	n, err := tr.RecoverInterrupted(ctx, "restart")
	if err != nil || n != 1 {
		t.Fatalf("recovered=%d err=%v", n, err)
	}
	got, _ := tr.Get(ctx, task.ID)
	if got.Status != StatusReady || got.Assignee != "" {
		t.Fatalf("task = %#v", got)
	}
	attempts, _ := tr.Attempts(ctx, task.ID)
	if len(attempts) != 1 || attempts[0].Status != "interrupted" {
		t.Fatalf("attempts = %#v", attempts)
	}
	_ = attempt
}

func TestQueueStateMeta(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	tr.SetQueueRunning(ctx, true)
	if !tr.QueueRunning(ctx) {
		t.Fatal("queue state not stored")
	}
	tr.SetQueueRunning(ctx, false)
	if tr.QueueRunning(ctx) {
		t.Fatal("queue state not cleared")
	}
}

func TestAssignBatchMakesTasksReady(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	a, _ := tr.Create(ctx, CreateInput{Title: "a", Complexity: 2, Status: StatusBacklog, CreatedBy: "master"}, "master")
	b, _ := tr.Create(ctx, CreateInput{Title: "b", Complexity: 2, Status: StatusBacklog, CreatedBy: "master"}, "master")
	if err := tr.AssignBatch(ctx, []string{a.ID, b.ID}, "run-1"); err != nil {
		t.Fatal(err)
	}
	tasks, err := tr.ByBatch(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d", len(tasks))
	}
	for _, task := range tasks {
		if task.Status != StatusReady || task.BatchID != "run-1" {
			t.Fatalf("task = %#v", task)
		}
	}
}

func TestFailedTaskCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tr, err := New(st.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := tr.Create(ctx, CreateInput{Title: "retry", Status: StatusReady, Complexity: 2, CreatedBy: "user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	task, err = tr.Transition(ctx, task.ID, StatusClaimed, "user")
	if err != nil {
		t.Fatal(err)
	}
	task, err = tr.Transition(ctx, task.ID, StatusInProgress, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.FailToBacklog(ctx, task.ID, "worker", "provider timeout"); err != nil {
		t.Fatal(err)
	}
	task, err = tr.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusBacklog || task.FailureReason != "provider timeout" || task.FailureAt == nil {
		t.Fatalf("failed task = status %s reason %q at %v", task.Status, task.FailureReason, task.FailureAt)
	}
	task, err = tr.Transition(ctx, task.ID, StatusReady, "user")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusReady || task.FailureReason != "" || task.FailureAt != nil {
		t.Fatalf("retried task = status %s reason %q at %v", task.Status, task.FailureReason, task.FailureAt)
	}
}
