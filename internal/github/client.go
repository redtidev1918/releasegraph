package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		endpoint := c.baseURL
		if !strings.HasSuffix(endpoint, "/") {
			endpoint += "/"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+strings.TrimPrefix(path, "/"), nil)
		if err != nil {
			return nil, nil, rgerrors.Wrap(rgerrors.Transient, "build GitHub request", err)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
	for next != "" {
		var page []map[string]any
		body, headers, err := c.get(ctx, next)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		values = append(values, page...)
		next = nextLink(headers.Get("Link"), c.baseURL)
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
