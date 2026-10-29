package usage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/texhik/conclave/internal/store"
)

func TestAggregatesByModelAndSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := New(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = u.Add(ctx, Record{SessionID: "s1", TaskID: "T-1", Provider: "p", Model: "m", Source: "worker", InputTokens: 100, OutputTokens: 10, Requests: 1, LastContext: 100, ContextWindow: 1000})
	_ = u.Add(ctx, Record{SessionID: "s1", Provider: "p", Model: "m", Source: "chat", InputTokens: 50, OutputTokens: 5, Requests: 1, LastContext: 50, ContextWindow: 1000})
	_ = u.Add(ctx, Record{SessionID: "s2", Provider: "p", Model: "m", Source: "chat", InputTokens: 25, OutputTokens: 2, Requests: 1})
	models, err := u.ByModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].InputTokens != 175 || models[0].Sessions != 2 {
		t.Fatalf("models = %#v", models)
	}
	sessions, err := u.BySession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %#v", sessions)
	}
	if sessions[0].InputTokens < 1 {
		t.Fatalf("session aggregate = %#v", sessions[0])
	}
}
