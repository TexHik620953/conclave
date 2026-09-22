package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/provider"
)

func TestAskUserOverWeb(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
			Stream   bool               `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		hasTool := false
		for _, m := range body.Messages {
			if m.Role == "tool" {
				hasTool = true
			}
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			if hasTool {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			} else {
				args := `{\"question\":\"Which DB?\",\"options\":[{\"label\":\"Postgres\"},{\"label\":\"SQLite\"}]}`
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"ask_user\",\"arguments\":\"%s\"}}]},\"finish_reason\":null}]}\n\n", args)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer llmSrv.Close()

	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: llmSrv.URL + "/v1"}},
		Roles:     map[string]config.Role{"r": {ID: "r", Model: "mock/m", Tools: []string{"ask_user"}}},
		Pipelines: map[string]config.Pipeline{
			"t": {Start: "a", Nodes: []config.Node{{ID: "a", Type: "agent", Role: "r", Output: "a.md"}}},
		},
		Settings: config.Settings{MaxParallel: 1, MaxIterations: 2, Timeout: config.Duration(5 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	engine := &orchestrator.Engine{
		Cfg: cfg,
		LLM: llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		Bus: bus,
	}
	srv := New(context.Background(), &engineDeps{cfg: cfg, bus: bus, engine: engine}, Options{})
	engine.Asker = srv.Broker()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.Read(ctx) // hello
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","run_id":""}`))
	time.Sleep(50 * time.Millisecond)

	payload, _ := json.Marshal(map[string]any{"pipeline": "t", "task": "x"})
	resp, err := http.Post(ts.URL+"/api/runs", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	runID := out["run_id"]

	// Wait for the question, answer it, then wait for completion.
	var qid string
	finished := false
	for !finished {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Data    struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		json.Unmarshal(data, &ev)
		switch ev.Type {
		case "user.question":
			qid = ev.Data.ID
			ans, _ := json.Marshal(map[string]any{"id": qid, "selected": []string{"Postgres"}})
			ar, err := http.Post(ts.URL+"/api/runs/"+runID+"/answer", "application/json", bytes.NewReader(ans))
			if err != nil {
				t.Fatal(err)
			}
			var acc map[string]bool
			json.NewDecoder(ar.Body).Decode(&acc)
			ar.Body.Close()
			if !acc["accepted"] {
				t.Fatal("answer not accepted")
			}
		case "run.finished":
			if ev.Message != "completed" {
				t.Fatalf("run finished with %q", ev.Message)
			}
			finished = true
		}
	}
	if qid == "" {
		t.Fatal("no question was asked")
	}
}
