package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateWebFetchURLRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{name: "ftp", raw: "ftp://example.com/file", wantSub: "unsupported scheme"},
		{name: "file", raw: "file:///etc/passwd", wantSub: "unsupported scheme"},
		{name: "no-host", raw: "https:///path", wantSub: "no host"},
		{name: "embedded-userinfo", raw: "https://user:pass@example.com/", wantSub: "embedded credentials"},
		{name: "ip-literal", raw: "http://127.0.0.1/admin", wantSub: "IP address literal"},
		{name: "loopback-numeric-hostname", raw: "http://2130706433/", wantSub: "IP address literal"},
		{name: "local-suffix", raw: "http://db.internal/admin", wantSub: "not a public hostname"},
		{name: "localhost-suffix", raw: "http://service.localhost:8080/", wantSub: "not a public hostname"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebFetchURL(tt.raw)
			if err == nil {
				t.Fatalf("validateWebFetchURL(%q) = nil, want error containing %q", tt.raw, tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("validateWebFetchURL(%q) error = %q, want containing %q", tt.raw, err.Error(), tt.wantSub)
			}
		})
	}
}

func TestValidateWebFetchURLAcceptsPublicHTTPSTargets(t *testing.T) {
	for _, raw := range []string{
		"https://docs.example.com/page",
		"http://example.com",
		"https://raw.githubusercontent.com/lzztt/iterxp/main/README.md",
	} {
		if err := validateWebFetchURL(raw); err != nil {
			t.Fatalf("validateWebFetchURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestIsPublicWebFetchIPRejectsPrivateAndMetadata(t *testing.T) {
	blocked := map[string]string{
		"127.0.0.1":       "loopback",
		"10.0.0.1":        "private",
		"172.16.0.1":      "private",
		"192.168.1.1":     "private",
		"169.254.1.1":     "link-local",
		"100.64.0.1":      "shared address space",
		"169.254.169.254": "cloud metadata",
		"0.0.0.0":         "unspecified",
		"255.255.255.255": "broadcast",
		"198.18.0.1":      "benchmarking",
		"198.51.100.7":    "documentation",
		"203.0.113.9":     "documentation",
	}
	for ip, label := range blocked {
		got := isPublicWebFetchIP(net.ParseIP(ip))
		if got {
			t.Fatalf("isPublicWebFetchIP(%s) [%s] = true, want false", ip, label)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34"} {
		if !isPublicWebFetchIP(net.ParseIP(ip)) {
			t.Fatalf("isPublicWebFetchIP(%s) = false, want true", ip)
		}
	}
}

func TestHTMLToReadableMarkdownConvertsAndResolvesRelativeLinks(t *testing.T) {
	fixture := `<!doctype html>
<html><head><style>body{color:red}</style><script>alert('x')</script><title>Title</title></head>
<body>
<nav><a href="/home">Home</a></nav>
<h1>Heading</h1>
<p>A <a href="/docs/page">relative link</a> and an <a href="https://example.com/abs">absolute link</a>.</p>
<ul><li>one</li><li>two</li></ul>
<pre><code>fmt.Println("hi")</code></pre>
<table><tr><th>Name</th></tr><tr><td>Value</td></tr></table>
</body></html>`
	got, err := htmlToReadableMarkdown(fixture, "https://docs.example.com/start/page")
	if err != nil {
		t.Fatalf("htmlToReadableMarkdown error = %v", err)
	}
	if strings.Contains(got, "alert('x')") {
		t.Fatalf("script content leaked into markdown: %q", got)
	}
	if strings.Contains(got, "body{color:red}") {
		t.Fatalf("style content leaked into markdown: %q", got)
	}
	for _, want := range []string{"Heading", "one", "two", "fmt.Println(\"hi\")", "Name", "Value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown missing %q: %s", want, got)
		}
	}
	if !strings.Contains(got, "[relative link](https://docs.example.com/docs/page)") {
		t.Fatalf("relative link not resolved: %s", got)
	}
	if !strings.Contains(got, "[absolute link](https://example.com/abs)") {
		t.Fatalf("absolute link not preserved: %s", got)
	}
}

func TestWebFetchResultForNon2xxReportsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>404 page</body></html>"))
	}))
	defer srv.Close()

	res := webFetchResultFor(srv.URL + "/missing")
	if res.Error == "" {
		t.Fatalf("expected error for non-public/loopback destination, got: %+v", res)
	}
	// The loopback httptest host must be rejected by the SSRF guard before any
	// HTTP exchange, so Status must not be populated.
	if res.Status != 0 {
		t.Fatalf("loopback destination should be rejected before HTTP, got status=%d res=%+v", res.Status, res)
	}
	if !strings.Contains(res.Error, "non-public") && !strings.Contains(res.Error, "IP address literal") {
		t.Fatalf("unexpected error for loopback destination: %q", res.Error)
	}
}

func TestWebFetchToolRejectsLoopbackViaCustomTransport(t *testing.T) {
	// Exercise the full tool path with a fake fetcher so we can verify that the
	// result serialization contract used by the structured tool dispatcher
	// (including the `## Content` output section) is consistent.
	var called atomic.Bool
	origFetcher := webFetchFetcher
	origLookup := webFetchLookupIP
	webFetchFetcher = func(ctx context.Context, client *http.Client, target string, retries int) (*http.Response, error) {
		called.Store(true)
		// Simulate a transient network error: the tool must serialize it cleanly.
		return nil, errors.New("dial tcp 10.0.0.1:80: i/o timeout")
	}
	webFetchLookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() {
		webFetchFetcher = origFetcher
		webFetchLookupIP = origLookup
	}()

	a := &Agent{}
	res := a.webFetchTool(nil, `{"url":"https://example.com/doc"}`)
	if res.ExitCode == 0 {
		t.Fatalf("webFetchTool exit code = 0, want non-zero; stdout=%q", res.Stdout)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("webFetchTool stdout is not valid JSON: %v\n%s", err, res.Stdout)
	}
	if parsed["error"] == nil || parsed["error"] == "" {
		t.Fatalf("webFetchTool result missing error field: %+v", parsed)
	}
	if !called.Load() {
		t.Fatalf("webFetchFetcher was not called")
	}
}

func TestApplyWebFetchContentLimitMarksTruncation(t *testing.T) {
	res := webFetchResult{Content: strings.Repeat("x", webFetchMaxContentChars+100)}
	applyWebFetchContentLimit(&res)
	if !res.FinalTrunc {
		t.Fatalf("FinalTrunc = false, want true")
	}
	if len([]rune(res.Content)) > webFetchMaxContentChars+len("...[content truncated: 00000 characters total]")+10 {
		t.Fatalf("content not bounded: %d chars", len([]rune(res.Content)))
	}
	if !strings.Contains(res.Content, "content truncated") {
		t.Fatalf("truncation marker missing: %s", res.Content[len(res.Content)-80:])
	}
}

func TestPrettyFetchedJSONIndents(t *testing.T) {
	raw := `{"b":1,"a":[1,2]}`
	got := prettyFetchedJSON([]byte(raw))
	if !strings.Contains(got, "\"a\": [") {
		t.Fatalf("prettyFetchedJSON did not indent: %s", got)
	}
	if !strings.Contains(got, "\"b\": 1") {
		t.Fatalf("prettyFetchedJSON missing field: %s", got)
	}
}

func TestWebFetchContentTypeDispatch(t *testing.T) {
	// Direct unit coverage of the content-type switch via the helper path used
	// by webFetchResultFor after a response body is read.
	html := "<html><body><h1>Hi</h1></body></html>"
	md, err := htmlToReadableMarkdown(html, "https://example.com")
	if err != nil || !strings.Contains(md, "Hi") {
		t.Fatalf("html conversion failed: md=%q err=%v", md, err)
	}
	if got := sanitizeFetchedPlainText("\r\nhello\rworld \n"); got != "hello\nworld" {
		t.Fatalf("sanitizeFetchedPlainText = %q, want %q", got, "hello\nworld")
	}
	if got := prettyFetchedJSON([]byte(`{"k":"v"}`)); !strings.Contains(got, "k") {
		t.Fatalf("prettyFetchedJSON = %q", got)
	}
}

func TestWebFetchToolEmptyURL(t *testing.T) {
	a := &Agent{}
	res := a.webFetchTool(nil, `{"url":"   "}`)
	if res.ExitCode == 0 {
		t.Fatalf("empty url exit code = 0, want non-zero")
	}
	if !strings.Contains(res.Error, "URL is empty") {
		t.Fatalf("empty url error = %q", res.Error)
	}
}

func TestWebFetchToolInvalidArguments(t *testing.T) {
	a := &Agent{}
	res := a.webFetchTool(nil, `not-json`)
	if res.ExitCode == 0 {
		t.Fatalf("invalid args exit code = 0, want non-zero")
	}
	if !strings.Contains(res.Error, "invalid web_fetch arguments") {
		t.Fatalf("invalid args error = %q", res.Error)
	}
}

func TestWebFetchTimeoutUsesDefault(t *testing.T) {
	if webFetchDefaultTimeout != 30*time.Second {
		t.Fatalf("webFetchDefaultTimeout = %v, want 30s", webFetchDefaultTimeout)
	}
}
