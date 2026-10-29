package search

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

	"github.com/texhik/conclave/internal/config"
)

type Result struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Snippet   string `json:"snippet,omitempty"`
	Published string `json:"published,omitempty"`
}

type Options struct {
	MaxResults     int
	Recency        string
	IncludeDomains []string
	ExcludeDomains []string
}

type Service struct {
	cfg    config.SearchConfig
	client *http.Client
}

func New(cfg config.SearchConfig) *Service {
	return &Service{cfg: cfg, client: &http.Client{Timeout: 35 * time.Second}}
}

func (s *Service) Search(ctx context.Context, query string, opts Options) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = s.cfg.MaxResults
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 5
	}
	if opts.MaxResults > 20 {
		opts.MaxResults = 20
	}
	switch strings.ToLower(strings.TrimSpace(s.cfg.Provider)) {
	case "", "tavily":
		return s.tavily(ctx, query, opts)
	case "brave":
		return s.brave(ctx, query, opts)
	case "searxng", "searx":
		return s.searxng(ctx, query, opts)
	case "exa":
		return s.exa(ctx, query, opts)
	default:
		return nil, fmt.Errorf("unknown search provider %q", s.cfg.Provider)
	}
}

func (s *Service) tavily(ctx context.Context, query string, opts Options) ([]Result, error) {
	if s.cfg.APIKey == "" {
		return nil, fmt.Errorf("search: TAVILY_API_KEY is not configured")
	}
	body := map[string]any{
		"api_key": s.cfg.APIKey, "query": query, "max_results": opts.MaxResults,
		"search_depth": "basic", "include_answer": false,
	}
	if len(opts.IncludeDomains) > 0 {
		body["include_domains"] = opts.IncludeDomains
	}
	if len(opts.ExcludeDomains) > 0 {
		body["exclude_domains"] = opts.ExcludeDomains
	}
	if days := recencyDays(opts.Recency); days > 0 {
		body["days"] = days
	}
	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}
	if err := s.postJSON(ctx, "https://api.tavily.com/search", nil, body, &out); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Content, Published: r.PublishedDate})
	}
	return results, nil
}

func (s *Service) brave(ctx context.Context, query string, opts Options) ([]Result, error) {
	if s.cfg.APIKey == "" {
		return nil, fmt.Errorf("search: BRAVE_API_KEY is not configured")
	}
	q := url.Values{"q": {query}, "count": {fmt.Sprint(opts.MaxResults)}}
	if freshness := recencyFreshness(opts.Recency); freshness != "" {
		q.Set("freshness", freshness)
	}
	headers := map[string]string{"X-Subscription-Token": s.cfg.APIKey, "Accept": "application/json"}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := s.getJSON(ctx, "https://api.search.brave.com/res/v1/web/search?"+q.Encode(), headers, &out); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Description, Published: r.Age})
	}
	return filterResults(results, opts), nil
}

func (s *Service) searxng(ctx context.Context, query string, opts Options) ([]Result, error) {
	base := strings.TrimRight(s.cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("search: searxng base_url is required")
	}
	rawURL := base + "/search"
	if !strings.HasSuffix(base, "/search") {
		rawURL = base + "/search"
	} else {
		rawURL = base
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if tr := recencyTimeRange(opts.Recency); tr != "" {
		q.Set("time_range", tr)
	}
	u.RawQuery = q.Encode()
	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := s.getJSON(ctx, u.String(), nil, &out); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Content, Published: r.PublishedDate})
	}
	return filterResults(results, opts), nil
}

func (s *Service) exa(ctx context.Context, query string, opts Options) ([]Result, error) {
	if s.cfg.APIKey == "" {
		return nil, fmt.Errorf("search: EXA_API_KEY is not configured")
	}
	body := map[string]any{
		"query": query, "numResults": opts.MaxResults,
		"contents": map[string]any{"text": map[string]any{"maxCharacters": 500}},
	}
	if len(opts.IncludeDomains) > 0 {
		body["includeDomains"] = opts.IncludeDomains
	}
	if len(opts.ExcludeDomains) > 0 {
		body["excludeDomains"] = opts.ExcludeDomains
	}
	headers := map[string]string{"x-api-key": s.cfg.APIKey}
	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Text          string `json:"text"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := s.postJSON(ctx, "https://api.exa.ai/search", headers, body, &out); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Text, Published: r.PublishedDate})
	}
	return results, nil
}

func (s *Service) getJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "conclave/0.1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		return fmt.Errorf("search HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(out)
}

func (s *Service) postJSON(ctx context.Context, rawURL string, headers map[string]string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "conclave/0.1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		return fmt.Errorf("search HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(out)
}

func filterResults(results []Result, opts Options) []Result {
	if len(opts.IncludeDomains) == 0 && len(opts.ExcludeDomains) == 0 {
		return results
	}
	out := make([]Result, 0, len(results))
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		allowed := len(opts.IncludeDomains) == 0
		for _, d := range opts.IncludeDomains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				allowed = true
				break
			}
		}
		for _, d := range opts.ExcludeDomains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				allowed = false
				break
			}
		}
		if allowed {
			out = append(out, r)
		}
	}
	return out
}

func recencyDays(v string) int {
	switch strings.ToLower(v) {
	case "day", "1d", "24h":
		return 1
	case "week", "1w", "7d":
		return 7
	case "month", "1m", "30d":
		return 30
	case "year", "1y", "365d":
		return 365
	default:
		return 0
	}
}

func recencyFreshness(v string) string {
	switch strings.ToLower(v) {
	case "day", "1d", "24h":
		return "pd"
	case "week", "1w", "7d":
		return "pw"
	case "month", "1m", "30d":
		return "pm"
	case "year", "1y", "365d":
		return "py"
	default:
		return ""
	}
}

func recencyTimeRange(v string) string {
	switch strings.ToLower(v) {
	case "day", "1d", "24h":
		return "day"
	case "week", "1w", "7d":
		return "week"
	case "month", "1m", "30d":
		return "month"
	case "year", "1y", "365d":
		return "year"
	default:
		return ""
	}
}
