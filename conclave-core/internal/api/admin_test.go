package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func adminJSON(t *testing.T, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("X-Admin-Key", "admin")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestAdminConfigAndRolesReadModel(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, prov := adminJSON(t, "POST", ts.URL+"/v1/admin/providers", map[string]any{
		"name": "deepseek", "base_url": "https://api.deepseek.com/v1", "api_key": "sk-secret",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create provider = %d %+v", resp.StatusCode, prov)
	}
	if _, hasKey := prov["api_key"]; hasKey {
		t.Fatal("provider response must not expose api_key")
	}
	pid, _ := prov["id"].(string)

	resp, model := adminJSON(t, "POST", ts.URL+"/v1/admin/providers/"+pid+"/models", map[string]any{
		"name": "deepseek-chat", "input_price_per_1m": 0.2, "output_price_per_1m": 0.5,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create model = %d %+v", resp.StatusCode, model)
	}
	mid, _ := model["id"].(string)

	resp, role := adminJSON(t, "POST", ts.URL+"/v1/admin/roles", map[string]any{
		"key": "backend", "title": "Backend", "control": "supervisor",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create role = %d %+v", resp.StatusCode, role)
	}
	rid, _ := role["id"].(string)

	resp, grade := adminJSON(t, "POST", ts.URL+"/v1/admin/roles/"+rid+"/grades", map[string]any{
		"grade": "senior", "rank": 2, "model_id": mid, "tools": []string{"read_file"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create grade = %d %+v", resp.StatusCode, grade)
	}

	resp, _ = adminJSON(t, "PUT", ts.URL+"/v1/admin/brain-configs/controller", map[string]any{
		"model_id": mid,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upsert brain = %d", resp.StatusCode)
	}

	// Public read model (device token) exposes the role with its model ref.
	token := bootstrap(t, ts)
	req, _ := http.NewRequest("GET", ts.URL+"/v1/roles", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var roles []map[string]any
	_ = json.NewDecoder(rresp.Body).Decode(&roles)
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK || len(roles) != 1 || roles[0]["key"] != "backend" {
		t.Fatalf("roles = %d %+v", rresp.StatusCode, roles)
	}
	grades, _ := roles[0]["grades"].([]any)
	if len(grades) != 1 {
		t.Fatalf("grades = %+v", roles[0]["grades"])
	}
	g0, _ := grades[0].(map[string]any)
	if g0["model"] != "deepseek/deepseek-chat" {
		t.Fatalf("grade model = %v", g0["model"])
	}
}
