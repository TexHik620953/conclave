package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestListSessionsEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	token := bootstrap(t, ts)

	resp, out := doJSON(t, "GET", ts.URL+"/v1/sessions", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions = %d", resp.StatusCode)
	}
	_ = out

	if r, _ := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "one"}); r.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", r.StatusCode)
	}
	if r, _ := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "two"}); r.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d", r.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	lresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []map[string]any
	_ = json.NewDecoder(lresp.Body).Decode(&sessions)
	lresp.Body.Close()
	if lresp.StatusCode != http.StatusOK || len(sessions) != 2 {
		t.Fatalf("sessions = %d %+v", lresp.StatusCode, sessions)
	}

	// Delete one session.
	id, _ := sessions[0]["id"].(string)
	dreq, _ := http.NewRequest("DELETE", ts.URL+"/v1/sessions/"+id, nil)
	dreq.Header.Set("Authorization", "Bearer "+token)
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK {
		t.Fatalf("delete session = %d", dresp.StatusCode)
	}
	lresp2, _ := doJSON(t, "GET", ts.URL+"/v1/sessions", token, nil)
	if lresp2.StatusCode != http.StatusOK {
		t.Fatalf("list after delete = %d", lresp2.StatusCode)
	}
}
