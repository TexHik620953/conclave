package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type webSearch struct{ env *Env }

func (t *webSearch) Name() string { return "web_search" }
func (t *webSearch) Description() string {
	return "Search the web and return the most relevant results (title, URL, snippet)."
}
func (t *webSearch) Schema() json.RawMessage {
	return schema(map[string]any{
		"query":       map[string]any{"type": "string", "description": "Search query."},
		"num_results": map[string]any{"type": "integer", "description": "Maximum number of results."},
	}, "query")
}

func (t *webSearch) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if t.env.WebSearch == nil {
		return Result{Content: "web_search is not configured; set settings.web_search in the config", IsError: true}, nil
	}
	if !t.env.Network {
		return Result{Content: "network permission denied", IsError: true}, nil
	}
	var in struct {
		Query      string `json:"query"`
		NumResults int    `json:"num_results"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return Result{Content: "query is required", IsError: true}, nil
	}
	cfg := t.env.WebSearch
	n := in.NumResults
	if n <= 0 {
		n = cfg.MaxResults
	}
	client := t.env.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	var results []searchResult
	var err error
	switch strings.ToLower(cfg.Provider) {
	case "searxng":
		results, err = searxngSearch(ctx, client, cfg.BaseURL, in.Query, n)
	default:
		results, err = exaSearch(ctx, client, cfg.BaseURL, cfg.APIKey, cfg.SearchType, in.Query, n)
	}
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if len(results) == 0 {
		return Result{Content: "no results"}, nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Published != "" {
			fmt.Fprintf(&b, "   published: %s\n", r.Published)
		}
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", oneLine(r.Snippet, 500))
		}
	}
	return Result{Content: truncate(t.env, b.String())}, nil
}

type searchResult struct {
	Title     string
	URL       string
	Snippet   string
	Published string
}

func exaSearch(ctx context.Context, client *http.Client, baseURL, apiKey, searchType, query string, n int) ([]searchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("exa api key is not set (EXA_API_KEY or settings.web_search.api_key)")
	}
	body, _ := json.Marshal(map[string]any{
		"query":      query,
		"numResults": n,
		"type":       searchType,
		"contents":   map[string]any{"highlights": true, "text": map[string]any{"maxCharacters": 1200}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("exa: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			PublishedDate string   `json:"publishedDate"`
			Text          string   `json:"text"`
			Highlights    []string `json:"highlights"`
			Summary       string   `json:"summary"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("exa: %w", err)
	}
	out := make([]searchResult, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		snippet := r.Summary
		if snippet == "" && len(r.Highlights) > 0 {
			snippet = strings.Join(r.Highlights, " … ")
		}
		if snippet == "" {
			snippet = r.Text
		}
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: snippet, Published: r.PublishedDate})
	}
	return out, nil
}

func searxngSearch(ctx context.Context, client *http.Client, baseURL, query string, n int) ([]searchResult, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("searxng: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	if n > len(parsed.Results) {
		n = len(parsed.Results)
	}
	out := make([]searchResult, 0, n)
	for _, r := range parsed.Results[:n] {
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
