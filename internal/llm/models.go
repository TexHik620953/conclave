package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/config"
)

type RemoteModel struct {
	ID        string `json:"id"`
	Context   int    `json:"context"`
	MaxOutput int    `json:"max_output"`
}

// ListModels queries the provider's OpenAI-compatible /models endpoint.
func ListModels(ctx context.Context, p config.Provider) ([]RemoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := strings.TrimRight(p.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	sink, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer sink.Body.Close()
	if sink.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(sink.Body, 16*1024))
		return nil, fmt.Errorf("provider /models: HTTP %d: %s", sink.StatusCode, strings.TrimSpace(string(b)))
	}
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	b, err := io.ReadAll(io.LimitReader(sink.Body, 8*1024*1024))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		// Some small gateways return a bare array.
		var arr []struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(b, &arr); err2 != nil {
			return nil, err
		}
		for _, m := range arr {
			if m.ID != "" {
				payload.Data = append(payload.Data, struct {
					ID            string `json:"id"`
					ContextLength int    `json:"context_length"`
					TopProvider   struct {
						MaxCompletionTokens int `json:"max_completion_tokens"`
					} `json:"top_provider"`
				}{ID: m.ID})
			}
		}
	}
	var out []RemoteModel
	seen := map[string]bool{}
	for _, m := range payload.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ctxLen := m.ContextLength
		if ctxLen <= 0 {
			ctxLen = 128000
		}
		maxOut := m.TopProvider.MaxCompletionTokens
		if maxOut <= 0 {
			maxOut = 8192
		}
		out = append(out, RemoteModel{ID: m.ID, Context: ctxLen, MaxOutput: maxOut})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("provider returned no models")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
