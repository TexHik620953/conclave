package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/config"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
)

func mockProvider(t *testing.T, tag string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, tag+":"+body.Model)
		resp := map[string]any{
			"model": body.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": tag},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestRouterRoutesByPrefix(t *testing.T) {
	var seen []string
	srvA := mockProvider(t, "A", &seen)
	defer srvA.Close()
	srvB := mockProvider(t, "B", &seen)
	defer srvB.Close()

	r, err := New(Config{
		Providers: map[string]config.Provider{
			"a":       {BaseURL: srvA.URL},
			"b":       {BaseURL: srvB.URL},
			"default": {BaseURL: srvA.URL},
		},
		DefaultProvider: "default",
	})
	if err != nil {
		t.Fatal(err)
	}

	// provider-prefixed model goes to provider B with the bare model name.
	resp, err := r.Complete(context.Background(), llmgw.Request{Model: "b/some/model"})
	if err != nil || resp.Message.Content != "B" {
		t.Fatalf("b: %+v err=%v", resp, err)
	}
	// unprefixed model uses the default provider.
	resp, err = r.Complete(context.Background(), llmgw.Request{Model: "plain"})
	if err != nil || resp.Message.Content != "A" {
		t.Fatalf("default: %+v err=%v", resp, err)
	}
	if len(seen) != 2 || seen[0] != "B:some/model" || seen[1] != "A:plain" {
		t.Fatalf("seen = %v", seen)
	}
}

func TestRouterUnknownProviderWithoutDefault(t *testing.T) {
	var seen []string
	srv := mockProvider(t, "A", &seen)
	defer srv.Close()
	r, err := New(Config{Providers: map[string]config.Provider{"a": {BaseURL: srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	// A single provider becomes the default.
	if _, err := r.Complete(context.Background(), llmgw.Request{Model: "x/y"}); err != nil {
		t.Fatalf("single provider should be the default: %v", err)
	}
}
