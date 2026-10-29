package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/domain"
)

func TestSessionAndRunRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	sess := &domain.Session{
		ID: "ses_1", Title: "test", Cwd: "/tmp", Mode: domain.ModeBuild,
		Model: "p/m", Effort: "medium", CreatedAt: time.Now(),
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
	}
	if err := s.SaveSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSession(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Fatalf("messages: %#v", got.Messages)
	}
	run := &domain.Run{ID: "run_1", SessionID: "ses_1", Phase: domain.RunPlanning, CreatedAt: time.Now()}
	if err := s.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	gotRun, err := s.LatestRun(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.ID != "run_1" {
		t.Fatalf("run id: %s", gotRun.ID)
	}
}
