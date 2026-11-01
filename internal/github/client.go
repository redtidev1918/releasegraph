package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New() *Client {
	base := os.Getenv("GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	return &Client{baseURL: strings.TrimRight(base, "/"), token: os.Getenv("GITHUB_TOKEN"), http: &http.Client{Timeout: 30 * time.Second}}
}

func NewForTest(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 5 * time.Second}}
}

func (c *Client) Get(ctx context.Context, path string, target any) error {
	body, _, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	if target != nil {
		return json.Unmarshal(body, target)
	}
	return nil
}

func (c *Client) GetOptional(ctx context.Context, path string, target any) (bool, error) {
	err := c.Get(ctx, path, target)
	if rgerrors.IsKind(err, rgerrors.NotFound) {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) ReadFile(ctx context.Context, repo, path, ref string) ([]byte, bool, error) {
	endpoint := fmt.Sprintf("repos/%s/contents/%s", repo, strings.TrimPrefix(path, "/"))
	if ref != "" {
		endpoint += "?ref=" + url.QueryEscape(ref)
	}
	var file struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	found, err := c.GetOptional(ctx, endpoint, &file)
	if err != nil || !found {
		return nil, found, err
	}
	if file.Encoding != "base64" {
		return nil, true, rgerrors.New(rgerrors.InvariantViolation, "GitHub content is not base64 encoded")
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, true, rgerrors.Wrap(rgerrors.InvariantViolation, "decode GitHub content", err)
	}
	return data, true, nil
}

func (c *Client) get(ctx context.Context, path string) ([]byte, http.Header, error) {
	return c.read(ctx, http.MethodGet, path, nil)
}

// read retries only read operations, including GraphQL queries.
func (c *Client) read(ctx context.Context, method, path string, payload []byte) ([]byte, http.Header, error) {
	var last error
	delay := time.Second
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			if delay > 60*time.Second {
				return nil, nil, fmt.Errorf("GitHub rate limit requires waiting %s; scan degraded", delay)
			}
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		endpoint := c.baseURL
		if !strings.HasSuffix(endpoint, "/") {
			endpoint += "/"
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint+strings.TrimPrefix(path, "/"), bytes.NewReader(payload))
		if err != nil {
			return nil, nil, rgerrors.Wrap(rgerrors.Transient, "build GitHub request", err)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		delay = time.Second << attempt
		resp, err := c.http.Do(req)
		if err != nil {
			last = rgerrors.Wrap(rgerrors.Transient, "GitHub request", err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			last = rgerrors.Wrap(rgerrors.Transient, "read GitHub response", readErr)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return body, resp.Header, nil
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 || (resp.StatusCode == 403 && (resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0")) {
			delay = retryDelay(resp.Header, delay, time.Now())
			last = rgerrors.New(rgerrors.Transient, fmt.Sprintf("GitHub %s", resp.Status))
			continue
		}
		if resp.StatusCode == 401 {
			return nil, nil, rgerrors.New(rgerrors.Authentication, githubErrorDetail("GitHub authentication failed", resp.StatusCode, body))
		}
		if resp.StatusCode == 403 {
			return nil, nil, rgerrors.New(rgerrors.Permission, githubErrorDetail("GitHub permission denied", resp.StatusCode, body))
		}
		if resp.StatusCode == 404 {
			return nil, nil, rgerrors.New(rgerrors.NotFound, "GitHub resource not found")
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			last = rgerrors.New(rgerrors.Transient, fmt.Sprintf("GitHub %s", resp.Status))
			continue
		}
		return nil, nil, rgerrors.New(rgerrors.Transient, fmt.Sprintf("GitHub %s: %s", resp.Status, strings.TrimSpace(string(body))))
	}
	return nil, nil, last
}

func (c *Client) Paginate(ctx context.Context, path string) ([]map[string]any, error) {
	values := []map[string]any{}
	next := path
	if !strings.Contains(next, "per_page=") {
		separator := "?"
		if strings.Contains(next, "?") {
			separator = "&"
		}
		next += separator + "per_page=100"
	}
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			return nil, fmt.Errorf("repeated pagination link")
		}
		seen[next] = true
		var page []map[string]any
		body, headers, err := c.get(ctx, next)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		if page == nil {
			return nil, fmt.Errorf("missing list page")
		}
		values = append(values, page...)
		next, err = checkedNextLink(headers.Get("Link"), c.baseURL)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (c *Client) Releases(ctx context.Context, repo string) ([]map[string]any, error) {
	var releases []map[string]any
	err := c.Get(ctx, fmt.Sprintf("repos/%s/releases?per_page=100", repo), &releases)
	return releases, err
}

func nextLink(header, baseURL string) string {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if len(fields) != 2 || !strings.Contains(fields[1], `rel="next"`) {
			continue
		}
		raw := strings.TrimSpace(fields[0])
		raw = strings.TrimPrefix(raw, "<")
		raw = strings.TrimSuffix(raw, ">")
		if _, err := url.Parse(raw); err != nil {
			return ""
		}
		base, err := url.Parse(baseURL)
		if err != nil {
			return ""
		}
		return strings.TrimPrefix(raw, base.String())
	}
	return ""
}

// githubErrorDetail renders a non-2xx response into an operator-readable
// message, keeping both the status code and the provider's own words.
//
// The body is preserved on purpose for the authorization failures: with a
// least-privilege GitHub App, a 403 body such as "Resource not accessible by
// integration" is the only thing that identifies which permission is missing.
func githubErrorDetail(label string, status int, body []byte) string {
	message := strings.TrimSpace(string(body))
	if message == "" {
		return fmt.Sprintf("%s (%d)", label, status)
	}
	return fmt.Sprintf("%s (%d): %s", label, status, message)
}

// A long server-directed delay exceeds this scan's request budget: fail degraded
// rather than retrying before the server permits it.
func retryDelay(h http.Header, fallback time.Duration, now time.Time) time.Duration {
	if n, err := strconv.Atoi(h.Get("Retry-After")); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(h.Get("Retry-After")); err == nil && at.After(now) {
		return at.Sub(now)
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(n, 0).After(now) {
			return time.Unix(n, 0).Sub(now)
		}
	}
	return fallback
}

// Query is read-only: callers supply a query operation, never a mutation.
func (b *Bound) Query(ctx context.Context, repo, query string, variables map[string]any, target any) error {
	if err := b.guardTarget(repo); err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(query), "query ") {
		return fmt.Errorf("only named GraphQL queries are allowed")
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || variables["owner"] != parts[0] || variables["name"] != parts[1] {
		return fmt.Errorf("GraphQL repository binding mismatch")
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	body, _, err := b.client.read(ctx, http.MethodPost, "graphql", payload)
	if err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err = json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("GraphQL: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("GraphQL data missing")
	}
	return json.Unmarshal(envelope.Data, target)
}

// Collection reads REST object-wrapped lists and verifies total_count.
func (b *Bound) Collection(ctx context.Context, path, key string) ([]map[string]any, error) {
	if err := b.guardPath(path); err != nil {
		return nil, err
	}
	values := []map[string]any{}
	seen := map[string]bool{}
	next := path
	separator := "?"
	if strings.Contains(next, "?") {
		separator = "&"
	}
	next += separator + "per_page=100"
	for next != "" {
		if seen[next] {
			return nil, fmt.Errorf("repeated pagination link")
		}
		seen[next] = true
		body, headers, err := b.client.get(ctx, next)
		if err != nil {
			return nil, err
		}
		var page map[string]json.RawMessage
		if err = json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		var total int
		if page["total_count"] == nil || page[key] == nil {
			return nil, fmt.Errorf("missing collection metadata")
		}
		if err = json.Unmarshal(page["total_count"], &total); err != nil {
			return nil, err
		}
		var entries []map[string]any
		if err = json.Unmarshal(page[key], &entries); err != nil {
			return nil, err
		}
		values = append(values, entries...)
		next, err = checkedNextLink(headers.Get("Link"), b.client.baseURL)
		if err != nil {
			return nil, err
		}
		if next == "" && len(values) != total {
			return nil, fmt.Errorf("incomplete %s collection: %d/%d", key, len(values), total)
		}
	}
	return values, nil
}

func checkedNextLink(header, baseURL string) (string, error) {
	for _, part := range strings.Split(header, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		fields := strings.Split(strings.TrimSpace(part), ";")
		if len(fields) < 2 {
			return "", fmt.Errorf("invalid pagination link")
		}
		raw := strings.TrimSpace(fields[0])
		if !strings.HasPrefix(raw, "<") || !strings.HasSuffix(raw, ">") {
			return "", fmt.Errorf("invalid pagination link")
		}
		target, err := url.Parse(strings.Trim(raw, "<>"))
		if err != nil {
			return "", err
		}
		base, err := url.Parse(baseURL)
		if err != nil {
			return "", err
		}
		if target.Scheme != base.Scheme || target.Host != base.Host || target.User != nil || !strings.HasPrefix(target.Path, strings.TrimRight(base.Path, "/")+"/") {
			return "", fmt.Errorf("pagination link escaped API origin")
		}
		return strings.TrimPrefix(target.String(), strings.TrimRight(baseURL, "/")), nil
	}
	return "", nil
}
