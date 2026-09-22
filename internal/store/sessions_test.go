package store

import "testing"

func TestSessionsAndMessages(t *testing.T) {
	s, err := Open("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateSession(Session{ID: "s1", Title: "hello", Pipeline: "solve"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(Run{ID: "r1", SessionID: "s1", Pipeline: "solve"}); err != nil {
		t.Fatal(err)
	}
	id1, err := s.AppendMessage(Message{SessionID: "s1", RunID: "r1", Role: "user", Kind: "user", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(Message{SessionID: "s1", RunID: "r1", Role: "senior", Kind: "text", Content: "working", Model: "mock/m"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListMessages("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != id1 || msgs[0].Content != "hi" || msgs[1].Kind != "text" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}

	runs, err := s.ListRunsBySession("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].SessionID != "s1" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	sessions, err := s.ListSessions(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "hello" {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}

	if err := s.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	msgs, _ = s.ListMessages("s1", 0)
	if len(msgs) != 0 {
		t.Fatalf("messages should be deleted with session, got %d", len(msgs))
	}
}

func TestMessageDataRoundTrip(t *testing.T) {
	s, err := Open("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession(Session{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(Message{
		SessionID: "s1", Role: "researcher", Kind: "tool_call",
		Data: map[string]any{"name": "web_search", "arguments": "{}"},
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListMessages("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Data["name"] != "web_search" {
		t.Fatalf("data not round-tripped: %+v", msgs)
	}
}
