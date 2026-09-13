package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
)

const (
	webFetchDefaultTimeout     = 30 * time.Second
	webFetchMaxRedirects       = 5
	webFetchMaxBodyBytes       = 2 << 20 // 2 MiB
	webFetchMaxContentChars    = 20000
	webFetchMaxEncodedURLRunes = 2048
)

// webFetchResult is the structured output returned by the web_fetch tool.
//
// The HTTP status is reported for any inspected response even when the body is
// truncated or unsupported, which lets the agent distinguish "200 with a huge
// page" from "404". For hard failures (network/validation errors) Status is 0
// and the failure is recorded in Error.
type webFetchResult struct {
	FinalURL    string `json:"final_url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Truncated   bool   `json:"truncated"`
	FinalTrunc  bool   `json:"final_truncated"`
	CharCount   int    `json:"char_count"`
	BodyBytes   int    `json:"body_bytes"`
	Content     string `json:"content,omitempty"`
	Error       string `json:"error,omitempty"`
}

var (
	errWebFetchStoppedAfterRedirects = errors.New("stopped after maximum redirects")
	errWebFetchRequestCancelled      = errors.New("web_fetch request was cancelled by the foreground process")
)

// webFetchFetcher fetches a single URL directly, without following redirects.
// Redirect handling lives in the client's CheckRedirect hook. It is a var so
// tests can swap in a controlled transport.
var webFetchFetcher = webFetchDo

// webFetchLookupIP is a seam for tests; production uses net.LookupIP.
var webFetchLookupIP = net.LookupIP

// validateWebFetchURL performs hostname-level rejection for the SSRF-guard
// fast path *and* for every redirect target seen by CheckRedirect.
func validateWebFetchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URL %q: %w", truncateWebFetch(raw, 256), err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}
	if u.User != nil {
		return fmt.Errorf("URL must not contain embedded credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no resolvable host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("destination IP address literal %q is not a public hostname", host)
	}
	if isNumericWebFetchHost(host) {
		return fmt.Errorf("destination IP address literal %q is not a public hostname", host)
	}
	lower := strings.ToLower(host)
	if strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".lan") ||
		strings.HasSuffix(lower, ".home") {
		return fmt.Errorf("destination host %q is not a public hostname", host)
	}
	return nil
}

func isNumericWebFetchHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validateWebFetchResolvedIP(hostport, host string) error {
	addrs, err := webFetchLookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve %q: no addresses", host)
	}
	for _, addr := range addrs {
		if !isPublicWebFetchIP(addr) {
			return fmt.Errorf("destination %q resolves to non-public address %s", hostport, addr.String())
		}
	}
	return nil
}

// isPublicWebFetchIP rejects loopback, private, link-local, unspecified,
// multicast, and cloud metadata addresses while allowing ordinary global
// unicast addresses.
func isPublicWebFetchIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return false
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return false // 100.64.0.0/10 shared address space
		}
		if ip4[0] == 169 && ip4[1] == 254 {
			return false // link-local (also covered above, kept explicit)
		}
		if ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0 {
			return false // 192.0.0.0/24
		}
		if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
			return false // benchmarking ranges
		}
		if ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100 {
			return false // documentation (TEST-NET-2)
		}
		if ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113 {
			return false // documentation (TEST-NET-3)
		}
		if ip4[0] == 255 {
			return false // 255.0.0.0/8
		}
	}
	return true
}

func limitWebFetchBody(u *url.URL, r io.Reader, limit int64) ([]byte, bool, error) {
	reader := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, fmt.Errorf("read response body from %s: %w", u.Host, err)
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func webFetchDo(ctx context.Context, client *http.Client, target string, retries int) (*http.Response, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, errWebFetchRequestCancelled
		default:
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if err == nil {
			err = errors.New("invalid URL")
		}
		return nil, err
	}
	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return nil, errWebFetchRequestCancelled
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "IterXP-web-fetch/1.0")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json,text/markdown,application/x-markdown;q=0.9,*/*;q=0.1")
		resp, err = client.Do(req)
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, errWebFetchRequestCancelled) {
			return nil, err
		}
		lastErr = err
		if urlErr, ok := err.(*url.Error); ok && urlErr.Temporary() {
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, lastErr
}

// webFetchCheckRedirect validates each redirect hop against the SSRF guard and
// the redirect-count limit. It is extracted so tests can exercise the redirect
// policy directly.
func webFetchCheckRedirect(registry map[string]struct{}, req *http.Request, via []*http.Request) error {
	if len(via) >= webFetchMaxRedirects {
		return fmt.Errorf("%w (%d)", errWebFetchStoppedAfterRedirects, webFetchMaxRedirects)
	}
	if req.URL == nil {
		return errors.New("redirect with no URL")
	}
	if err := validateWebFetchURL(req.URL.String()); err != nil {
		return err
	}
	host := req.URL.Hostname()
	if _, seen := registry[host]; !seen {
		registry[host] = struct{}{}
		if err := validateWebFetchResolvedIP(req.URL.Host, host); err != nil {
			return err
		}
	}
	return nil
}

// webFetchResultFor fetches a single URL with the standard bounds.
// The SSRF guard runs on both the original host and every redirect target.
func webFetchResultFor(input string) webFetchResult {
	input = strings.TrimSpace(input)
	if input == "" {
		return webFetchResult{Error: "web_fetch URL is empty"}
	}
	if len([]rune(input)) > webFetchMaxEncodedURLRunes {
		return webFetchResult{Error: "web_fetch URL is too long"}
	}
	if err := validateWebFetchURL(input); err != nil {
		return webFetchResult{Error: err.Error()}
	}

	orig, err := url.Parse(input)
	if err != nil {
		return webFetchResult{Error: "parse URL: " + err.Error()}
	}
	if err := validateWebFetchResolvedIP(orig.Host, orig.Hostname()); err != nil {
		return webFetchResult{Error: err.Error()}
	}
	registry := map[string]struct{}{orig.Hostname(): {}}

	client := &http.Client{
		Timeout: webFetchDefaultTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return webFetchCheckRedirect(registry, req, via)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), webFetchDefaultTimeout)
	defer cancel()
	resp, err := webFetchFetcher(ctx, client, input, 0)
	if err != nil {
		return webFetchResult{Error: webFetchErrorString(input, err)}
	}
	defer resp.Body.Close()

	finalURL := input
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := ""
	if resp.Header != nil {
		contentType = resp.Header.Get("Content-Type")
	}
	mediaType := ""
	if i := strings.Index(contentType, ";"); i >= 0 {
		mediaType = strings.TrimSpace(contentType[:i])
	} else {
		mediaType = strings.TrimSpace(contentType)
	}
	mediaType = strings.ToLower(mediaType)

	if !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return webFetchResult{
			FinalURL:    finalURL,
			Status:      resp.StatusCode,
			ContentType: contentType,
		}
	}

	body, bodyTruncated, err := limitWebFetchBody(resp.Request.URL, resp.Body, webFetchMaxBodyBytes)
	if err != nil {
		return webFetchResult{
			FinalURL:    finalURL,
			Status:      resp.StatusCode,
			ContentType: contentType,
			Error:       err.Error(),
		}
	}

	switch mediaType {
	case "text/html", "application/xhtml+xml":
		markdown, convertErr := htmlToReadableMarkdown(string(body), finalURL)
		if convertErr != nil {
			return webFetchResult{
				FinalURL:    finalURL,
				Status:      resp.StatusCode,
				ContentType: contentType,
				BodyBytes:   len(body),
				Truncated:   bodyTruncated,
				Error:       "convert HTML to markdown: " + convertErr.Error(),
			}
		}
		res := webFetchResult{
			FinalURL:    finalURL,
			Status:      resp.StatusCode,
			ContentType: contentType,
			BodyBytes:   len(body),
			Truncated:   bodyTruncated,
			Content:     markdown,
		}
		applyWebFetchContentLimit(&res)
		return res

	case "text/plain", "text/markdown", "application/x-markdown", "application/markdown":
		res := webFetchResult{
			FinalURL:    finalURL,
			Status:      resp.StatusCode,
			ContentType: contentType,
			BodyBytes:   len(body),
			Truncated:   bodyTruncated,
			Content:     sanitizeFetchedPlainText(string(body)),
		}
		applyWebFetchContentLimit(&res)
		return res

	case "application/json":
		res := webFetchResult{
			FinalURL:    finalURL,
			Status:      resp.StatusCode,
			ContentType: contentType,
			BodyBytes:   len(body),
			Truncated:   bodyTruncated,
			Content:     prettyFetchedJSON(body),
		}
		applyWebFetchContentLimit(&res)
		return res

	default:
		return webFetchResult{
			FinalURL:    finalURL,
			Status:      resp.StatusCode,
			ContentType: contentType,
			BodyBytes:   len(body),
			Truncated:   bodyTruncated,
			Error:       fmt.Sprintf("unsupported content type %q; only HTML, plain text, Markdown, and JSON are returned", mediaType),
		}
	}
}

func applyWebFetchContentLimit(res *webFetchResult) {
	if res == nil {
		return
	}
	res.CharCount = len([]rune(res.Content))
	if res.CharCount <= webFetchMaxContentChars {
		return
	}
	runes := []rune(res.Content)
	res.Content = string(runes[:webFetchMaxContentChars]) + fmt.Sprintf("\n...[content truncated: %d characters total]", res.CharCount)
	res.CharCount = len([]rune(res.Content))
	res.FinalTrunc = true
}

func webFetchErrorString(input string, err error) string {
	return "fetch " + truncateWebFetch(input, 256) + ": " + err.Error()
}

func truncateWebFetch(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func sanitizeFetchedPlainText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	return s
}

func prettyFetchedJSON(data []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return string(data)
	}
	return out.String()
}

// webFetchBoilerplatePlugin removes obvious site navigation boilerplate that is
// not part of the readable article body.
type webFetchBoilerplatePlugin struct{}

func (webFetchBoilerplatePlugin) Name() string { return "webfetch-boilerplate" }

func (webFetchBoilerplatePlugin) Init(conv *converter.Converter) error {
	for _, tag := range []string{"nav", "header", "footer", "aside"} {
		conv.Register.TagType(tag, converter.TagTypeRemove, converter.PriorityStandard)
	}
	return nil
}

func htmlToReadableMarkdown(htmlInput, finalURL string) (string, error) {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
			webFetchBoilerplatePlugin{},
		),
	)
	return conv.ConvertString(htmlInput, converter.WithDomain(finalURL))
}

// webFetchTool runs the tool and converts the structured result to the
// ToolResult contract used by executeToolCall.
func (a *Agent) webFetchTool(session *Session, arguments string) ToolResult {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ToolResult{ExitCode: -1, Error: "invalid web_fetch arguments: " + err.Error()}
	}
	if strings.TrimSpace(args.URL) == "" {
		return ToolResult{ExitCode: -1, Error: "web_fetch URL is empty"}
	}
	res := webFetchResultFor(args.URL)
	resContent := res.Content
	res.Content = ""
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return ToolResult{ExitCode: -1, Error: "marshal web_fetch result: " + err.Error()}
	}
	out := string(data) + "\n"
	if resContent != "" {
		out += "\n## Content\n" + resContent + "\n"
	}
	code := 0
	if res.Error != "" {
		code = -1
	}
	return ToolResult{Stdout: out, Stderr: "", ExitCode: code}
}
