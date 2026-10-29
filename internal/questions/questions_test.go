package questions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tracker"
)

func TestAutonomousQuestionBlocksAndUnblocksTask(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tr, err := tracker.New(st.DB(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	task, err := tr.Create(context.Background(), tracker.CreateInput{Title: "q", Complexity: 2, Status: tracker.StatusReady, CreatedBy: "worker"}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(st.DB(), tr, event.New())
	if err != nil {
		t.Fatal(err)
	}
	set, err := q.Create(context.Background(), Set{TaskID: task.ID, Title: "Need decision", Category: "decision", Questions: []Question{{ID: "q1", Text: "Which?", Type: "single", Required: true, Options: []Option{{ID: "a", Label: "A"}}}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := tr.Get(context.Background(), task.ID)
	if got.Status != tracker.StatusBlocked {
		t.Fatalf("status = %s", got.Status)
	}
	_, err = q.Answer(context.Background(), set.ID, map[string]Question{"q1": {ID: "q1", Selected: []string{"a"}}}, "human")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = tr.Get(context.Background(), task.ID)
	if got.Status != tracker.StatusReady {
		t.Fatalf("after answer status = %s", got.Status)
	}
	answered, err := q.Get(context.Background(), set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != "answered" || answered.AnsweredBy != "human" {
		t.Fatalf("answer metadata = status %s by %q", answered.Status, answered.AnsweredBy)
	}
}
