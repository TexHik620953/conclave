package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchExa(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "Go docs", "url": "https://go.dev", "highlights": []string{"the Go language"}},
			},
		})
	}))
	defer srv.Close()

	env := &Env{
		Network: true,
		WebSearch: &WebSearchConfig{
			Provider:   "exa",
			BaseURL:    srv.URL,
			APIKey:     "secret",
			MaxResults: 3,
			SearchType: "auto",
		},
	}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{"query": "go"})
	res, err := reg.Execute(context.Background(), "web_search", args)
	if err != nil || res.IsError {
		t.Fatalf("web_search: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "Go docs") || !strings.Contains(res.Content, "https://go.dev") {
		t.Fatalf("unexpected content: %s", res.Content)
	}
}

func TestWebSearchNotConfigured(t *testing.T) {
	reg := NewRegistry(&Env{Network: true})
	res, err := reg.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error when web_search is not configured")
	}
}
