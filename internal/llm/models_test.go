package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/texhik/conclave/internal/config"
)

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-4o","context_length":128000,"top_provider":{"max_completion_tokens":4096}},
			{"id":"deepseek/deepseek-chat"}
		]}`))
	}))
	defer srv.Close()
	models, err := ListModels(context.Background(), config.Provider{BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "deepseek/deepseek-chat" {
		t.Fatalf("models = %#v", models)
	}
	if models[1].Context != 128000 || models[1].MaxOutput != 4096 {
		t.Fatalf("metadata = %#v", models[1])
	}
}
