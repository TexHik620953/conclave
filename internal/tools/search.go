package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/search"
)

type webSearch struct {
	svc *search.Service
}

func NewWebSearch(cfg config.SearchConfig) Tool {
	return webSearch{svc: search.New(cfg)}
}

func AddWebSearch(r *Registry, cfg config.SearchConfig) {
	r.Add(NewWebSearch(cfg))
}

// AddWebTools keeps read-only internet access available even when a worker
// restricts its tool allowlist.
func AddWebTools(r *Registry, cfg config.SearchConfig) {
	r.Add(webFetch{})
	AddWebSearch(r, cfg)
}

func (webSearch) Name() string { return "web_search" }
func (webSearch) Spec() llm.Tool {
	return llm.Tool{
		Name:        "web_search",
		Description: "Search the web. Returns titles, URLs, snippets and publication dates.",
		Parameters: schema(map[string]any{
			"query":           prop("string", "Search query"),
			"max_results":     prop("integer", "Maximum results, 1..20"),
			"recency":         prop("string", "Optional recency: day, week, month or year"),
			"include_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"exclude_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "query"),
	}
}
func (t webSearch) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	if env.Permissions.Network == "deny" {
		return "", fmt.Errorf("network access is disabled by permissions")
	}
	var in struct {
		Query          string   `json:"query"`
		MaxResults     int      `json:"max_results"`
		Recency        string   `json:"recency"`
		IncludeDomains []string `json:"include_domains"`
		ExcludeDomains []string `json:"exclude_domains"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	results, err := t.svc.Search(ctx, in.Query, search.Options{
		MaxResults: in.MaxResults, Recency: in.Recency,
		IncludeDomains: in.IncludeDomains, ExcludeDomains: in.ExcludeDomains,
	})
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No results.", nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n%s\n", i+1, r.Title, r.URL)
		if r.Published != "" {
			fmt.Fprintf(&b, "Published: %s\n", r.Published)
		}
		if r.Snippet != "" {
			fmt.Fprintf(&b, "%s\n", r.Snippet)
		}
		b.WriteString("\n")
	}
	return clip(b.String(), 60_000), nil
}
