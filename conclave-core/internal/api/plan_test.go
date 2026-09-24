package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/llmgw"
)

func toolResp(name, args string) llmgw.Response {
	return llmgw.Response{Message: llmgw.Message{
		Role:      "assistant",
		ToolCalls: []llmgw.ToolCall{{ID: "1", Name: name, Arguments: args}},
	}}
}

func bootstrap(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/bootstrap", strings.NewReader(`{"tenant_name":"Acme"}`))
	req.Header.Set("X-Admin-Key", "admin")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&boot)
	resp.Body.Close()
	return boot["token"]
}

func TestPlanGateFlow(t *testing.T) {
	ts, deps := newTestServer(t)
	token := bootstrap(t, ts)

	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "plan"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", resp.StatusCode)
	}
	sid, _ := sess["id"].(string)

	body := map[string]any{
		"nodes": []map[string]any{
			{"id": "a", "playbook_id": "ba"},
			{"id": "g", "kind": "gate", "title": "Approve requirements?"},
			{"id": "b", "playbook_id": "general"},
		},
		"edges": []map[string]any{
			{"from": "a", "to": "g"},
			{"from": "a", "to": "b"},
		},
	}
	resp, out := doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/plans", token, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create plan = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)

	// One open question.
	qreq, _ := http.NewRequest("GET", ts.URL+"/v1/sessions/"+sid+"/questions", nil)
	qreq.Header.Set("Authorization", "Bearer "+token)
	qresp, _ := http.DefaultClient.Do(qreq)
	var questions []map[string]any
	_ = json.NewDecoder(qresp.Body).Decode(&questions)
	qresp.Body.Close()
	if len(questions) != 1 || questions[0]["state"] != "open" {
		t.Fatalf("questions = %+v", questions)
	}
	qid, _ := questions[0]["id"].(string)

	// Answer and resume (asynchronous).
	resp, out = doJSON(t, "POST", ts.URL+"/v1/questions/"+qid+"/answer", token, map[string]any{"selected": []string{"Approve"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("answer = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)

	// Read model shows the finished graph.
	resp, view := doJSON(t, "GET", ts.URL+"/v1/sessions/"+sid+"/plan", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan = %d", resp.StatusCode)
	}
	nodes, _ := view["nodes"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	for _, raw := range nodes {
		n := raw.(map[string]any)
		if n["state"] != "done" {
			t.Fatalf("node %v state = %v", n["id"], n["state"])
		}
	}
}

func TestInterviewFlow(t *testing.T) {
	ts, deps := newTestServer(t)
	token := bootstrap(t, ts)

	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "shop"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", resp.StatusCode)
	}
	sid, _ := sess["id"].(string)

	// Start the interview (background job); the interviewer asks a question.
	resp, out := doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/interview", token, map[string]any{"idea": "build a shop"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("interview = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)

	qreq, _ := http.NewRequest("GET", ts.URL+"/v1/sessions/"+sid+"/questions", nil)
	qreq.Header.Set("Authorization", "Bearer "+token)
	qresp, _ := http.DefaultClient.Do(qreq)
	var questions []map[string]any
	_ = json.NewDecoder(qresp.Body).Decode(&questions)
	qresp.Body.Close()
	if len(questions) != 1 {
		t.Fatalf("questions = %+v", questions)
	}
	qid, _ := questions[0]["id"].(string)

	// Answer: the interviewer writes the spec (background job).
	resp, out = doJSON(t, "POST", ts.URL+"/v1/questions/"+qid+"/answer", token, map[string]any{"selected": []string{"Go"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("answer = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)

	// The spec is stored on the session.
	resp, detail := doJSON(t, "GET", ts.URL+"/v1/sessions/"+sid, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session = %d", resp.StatusCode)
	}
	spec, _ := detail["spec"].(map[string]any)
	if spec == nil || spec["content"] == "" {
		t.Fatalf("spec = %+v", detail["spec"])
	}

	// Auto-plan from the spec (background job).
	resp, out = doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/plans", token, map[string]any{"auto": true})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("auto plan = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)

	resp, view := doJSON(t, "GET", ts.URL+"/v1/sessions/"+sid+"/plan", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan = %d", resp.StatusCode)
	}
	if nodes, _ := view["nodes"].([]any); len(nodes) == 0 {
		t.Fatalf("auto plan created no nodes: %+v", view)
	}
}

func TestReplanFlow(t *testing.T) {
	ts, deps := newTestServer(t)
	token := bootstrap(t, ts)
	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "replan"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", resp.StatusCode)
	}
	sid, _ := sess["id"].(string)

	resp, out := doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/plans", token, map[string]any{
		"nodes": []map[string]any{{"id": "a", "playbook_id": "ba"}},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create plan = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)
	plan, _ := out["plan"].(map[string]any)
	planID, _ := plan["id"].(string)

	// Replan: the test planner returns node "a" again, so the version bumps and
	// the finished node keeps its state.
	resp, out = doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/plans/"+planID+"/replan", token, map[string]any{"reason": "add tests"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("replan = %d %+v", resp.StatusCode, out)
	}
	drainJobs(t, deps)
	newPlan, _ := out["plan"].(map[string]any)
	if v, _ := newPlan["version"].(float64); v != 2 {
		t.Fatalf("version = %v, want 2", newPlan["version"])
	}
}

func TestUserTokenEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/bootstrap", strings.NewReader(`{"tenant_name":"Acme"}`))
	req.Header.Set("X-Admin-Key", "admin")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&boot)
	resp.Body.Close()
	if boot["user_id"] == "" {
		t.Fatalf("bootstrap missing user_id: %+v", boot)
	}

	// Requires the admin key.
	resp2, _ := doJSON(t, "POST", ts.URL+"/v1/users/token", "", map[string]any{"user_id": boot["user_id"]})
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("user token without admin key = %d, want 401", resp2.StatusCode)
	}

	req3, _ := http.NewRequest("POST", ts.URL+"/v1/users/token", strings.NewReader(`{"user_id":"`+boot["user_id"]+`"}`))
	req3.Header.Set("X-Admin-Key", "admin")
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	_ = json.NewDecoder(resp3.Body).Decode(&out)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusCreated || out["user_token"] == "" {
		t.Fatalf("user token = %d %+v", resp3.StatusCode, out)
	}
}

func TestInterruptSession(t *testing.T) {
	ts, _ := newTestServer(t)
	token := bootstrap(t, ts)
	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "int"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", resp.StatusCode)
	}
	sid, _ := sess["id"].(string)
	resp, out := doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/plans", token, map[string]any{
		"nodes": []map[string]any{{"id": "a", "playbook_id": "ba"}},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create plan = %d %+v", resp.StatusCode, out)
	}
	resp, out = doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/interrupt", token, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("interrupt = %d %+v", resp.StatusCode, out)
	}
	if n, _ := out["interrupted"].(float64); n < 1 {
		t.Fatalf("interrupted = %v, want >= 1", out["interrupted"])
	}
}

func TestFollowupMessage(t *testing.T) {
	ts, deps := newTestServer(t)
	token := bootstrap(t, ts)
	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "chat"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", resp.StatusCode)
	}
	sid, _ := sess["id"].(string)

	resp, out := doJSON(t, "POST", ts.URL+"/v1/sessions/"+sid+"/message", token, map[string]any{"content": "build a shop"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("message = %d %+v", resp.StatusCode, out)
	}
	if out["job"] == nil {
		t.Fatalf("no job returned: %+v", out)
	}
	drainJobs(t, deps)

	resp, view := doJSON(t, "GET", ts.URL+"/v1/sessions/"+sid+"/plan", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plan = %d", resp.StatusCode)
	}
	if nodes, _ := view["nodes"].([]any); len(nodes) == 0 {
		t.Fatalf("no plan nodes after message: %+v", view)
	}
}

func TestDeviceMe(t *testing.T) {
	ts, _ := newTestServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/bootstrap", strings.NewReader(`{"tenant_name":"Acme","device_name":"laptop"}`))
	req.Header.Set("X-Admin-Key", "admin")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&boot)
	resp.Body.Close()

	dreq, _ := http.NewRequest("GET", ts.URL+"/v1/devices/me", nil)
	dreq.Header.Set("Authorization", "Bearer "+boot["token"])
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	defer dresp.Body.Close()
	var me map[string]string
	_ = json.NewDecoder(dresp.Body).Decode(&me)
	if dresp.StatusCode != http.StatusOK || me["device_id"] != boot["device_id"] || me["user_id"] != boot["user_id"] {
		t.Fatalf("device/me = %d %+v (want device %s user %s)", dresp.StatusCode, me, boot["device_id"], boot["user_id"])
	}
}
