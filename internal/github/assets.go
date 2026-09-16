package github

import (
	"context"
	"io"
	"net/http"
	"strconv"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// ReleaseAssetText downloads a small release asset (e.g. SHA256SUMS) by asset id.
func (c *Client) ReleaseAssetText(ctx context.Context, repo string, assetID int64) (string, bool, error) {
	endpoint := c.baseURL + "/repos/" + repo + "/releases/assets/" + strconv.FormatInt(assetID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, rgerrors.Wrap(rgerrors.Transient, "build asset request", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, rgerrors.Wrap(rgerrors.Transient, "download asset", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode >= 300 {
		return "", false, rgerrors.New(rgerrors.Transient, http.StatusText(resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false, rgerrors.Wrap(rgerrors.Transient, "read asset", err)
	}
	return string(data), true, nil
}
