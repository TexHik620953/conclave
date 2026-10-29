package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/texhik/conclave/internal/llm"
)

type webFetch struct{}

func (webFetch) Name() string { return "web_fetch" }
func (webFetch) Spec() llm.Tool {
	return llm.Tool{Name: "web_fetch", Description: "Fetch a URL and return readable Markdown, JSON or plain text.",
		Parameters: schema(map[string]any{"url": prop("string", "HTTP or HTTPS URL")}, "url")}
}
func (webFetch) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	if env.Permissions.Network == "deny" {
		return "", fmt.Errorf("network access is disabled by permissions")
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("only http(s) URLs are allowed")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "conclave/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain;q=0.9,*/*;q=0.5")
	client := &http.Client{
		Transport: &http.Transport{DialContext: safeDialContext(env.Permissions.AllowPrivateNetwork)},
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http URL is not allowed")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2_000_000))
	if err != nil {
		return "", err
	}
	ct := resp.Header.Get("Content-Type")
	body := string(b)
	switch {
	case strings.Contains(ct, "html") || strings.Contains(ct, "xml"):
		body = htmlToMarkdown(body)
	case strings.Contains(ct, "json"):
		var v any
		if json.Unmarshal(b, &v) == nil {
			if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
				body = string(pretty)
			}
		}
	}
	return clip(fmt.Sprintf("status: %d\nurl: %s\ncontent-type: %s\n\n%s", resp.StatusCode, resp.Request.URL.String(), ct, body), 120_000), nil
}

func safeDialContext(allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !allowPrivate && isPrivateIP(ip.IP) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		}
		return nil, fmt.Errorf("refusing to connect to private or local address %s", host)
	}
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func htmlToMarkdown(source string) string {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return stripHTML(source)
	}
	var b strings.Builder
	var walk func(*html.Node)
	newline := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg", "nav", "footer", "header":
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				newline()
				b.WriteString(strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			case "p", "div", "section", "article", "br":
				newline()
			case "li":
				newline()
				b.WriteString("- ")
			case "pre":
				newline()
				b.WriteString("```\n")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						b.WriteString(c.Data)
					} else {
						walk(c)
					}
				}
				b.WriteString("\n```\n")
				return
			case "code":
				if n.Parent == nil || n.Parent.Data != "pre" {
					b.WriteString("`")
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.TextNode {
							b.WriteString(c.Data)
						} else {
							walk(c)
						}
					}
					b.WriteString("`")
					return
				}
			case "a":
				text := strings.TrimSpace(nodeText(n))
				href := ""
				for _, a := range n.Attr {
					if a.Key == "href" {
						href = a.Val
					}
				}
				if text != "" {
					appendText(&b, text)
					if href != "" && strings.HasPrefix(href, "http") {
						b.WriteString(" (" + href + ")")
					}
				}
				return
			case "img":
				for _, a := range n.Attr {
					if a.Key == "alt" && a.Val != "" {
						b.WriteString("[image: " + a.Val + "]")
					}
				}
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.Join(strings.Fields(n.Data), " ")
			appendText(&b, text)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		switch n.Data {
		case "h1", "h2", "h3", "h4", "h5", "h6", "p", "div", "section", "article", "li", "ul", "ol", "table", "tr":
			newline()
		}
	}
	walk(doc)
	out := strings.TrimSpace(b.String())
	return strings.ReplaceAll(out, "\n\n\n", "\n\n")
}

func appendText(b *strings.Builder, text string) {
	if text == "" {
		return
	}
	if b.Len() > 0 {
		last := b.String()[b.Len()-1]
		first := text[0]
		if !strings.ContainsRune(" \n\t", rune(last)) && !strings.ContainsRune(".,;:!?)]}", rune(first)) {
			b.WriteString(" ")
		}
	}
	b.WriteString(text)
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func stripHTML(s string) string {
	repl := strings.NewReplacer(
		"<script", "\n<script", "</script>", "</script>\n",
		"<style", "\n<style", "</style>", "</style>\n",
		"<p", "\n<p", "<br", "\n<br", "<div", "\n<div", "<li", "\n<li",
	)
	s = repl.Replace(s)
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
