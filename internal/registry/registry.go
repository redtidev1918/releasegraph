package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

type Verifier struct {
	httpClient *http.Client
	npmBase    string
	pypiBase   string
	pubBase    string
	ghcrBase   string
}

func New() *Verifier {
	return &Verifier{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		npmBase:    "https://registry.npmjs.org",
		pypiBase:   "https://pypi.org/pypi",
		pubBase:    "https://pub.dev/api/packages",
		ghcrBase:   "https://ghcr.io",
	}
}

func NewForTest(base string) *Verifier {
	v := New()
	v.npmBase = base
	v.pypiBase = base + "/pypi"
	v.pubBase = base + "/pub"
	v.ghcrBase = base
	return v
}

func (v *Verifier) Verify(ctx context.Context, name string, config policy.Registry, repository, version string, metadata []byte) error {
	switch name {
	case "github":
		return nil
	case "npm", "pypi", "pub":
		packageName := packageName(name, repository, metadata)
		if name == "npm" {
			return v.expectStatus(ctx, v.npmBase+"/"+url.PathEscape(packageName)+"/"+url.PathEscape(version), packageName+"@"+version)
		}
		if name == "pypi" {
			return v.expectStatus(ctx, v.pypiBase+"/"+url.PathEscape(packageName)+"/"+url.PathEscape(version)+"/json", packageName+"=="+version)
		}
		endpoint := v.pubBase + "/" + url.PathEscape(packageName)
		data, found, err := v.get(ctx, endpoint)
		if err != nil || !found {
			return err
		}
		var payload struct {
			Versions map[string]json.RawMessage `json:"versions"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return rgerrors.Wrap(rgerrors.Transient, "parse pub response", err)
		}
		if _, ok := payload.Versions[version]; !ok {
			return rgerrors.New(rgerrors.RegistryConflict, packageName+" "+version+" is not published")
		}
		return nil
	case "ghcr":
		return v.verifyGHCR(ctx, config.Image, version)
	default:
		return rgerrors.New(rgerrors.Policy, "unsupported registry: "+name)
	}
}

func (v *Verifier) expectStatus(ctx context.Context, endpoint, label string) error {
	_, found, err := v.get(ctx, endpoint)
	if err != nil {
		return err
	}
	if !found {
		return rgerrors.New(rgerrors.RegistryConflict, label+" is not published")
	}
	return nil
}

func (v *Verifier) get(ctx context.Context, endpoint string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, false, rgerrors.Wrap(rgerrors.Transient, "registry request", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, false, rgerrors.New(rgerrors.Transient, resp.Status)
	}
	if resp.StatusCode >= 300 {
		return nil, false, rgerrors.New(rgerrors.RegistryConflict, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, rgerrors.Wrap(rgerrors.Transient, "read registry response", err)
	}
	return data, true, nil
}

func packageName(kind, repository string, metadata []byte) string {
	pattern := regexp.MustCompile(`(?m)^\s*name\s*=\s*["']([^"']+)["']`)
	if kind == "npm" || kind == "pub" {
		pattern = regexp.MustCompile(`(?m)^\s*"name"\s*:\s*"([^"]+)"`)
	}
	if match := pattern.FindSubmatch(metadata); len(match) > 1 {
		return string(match[1])
	}
	name := repository[strings.LastIndex(repository, "/")+1:]
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

func (v *Verifier) verifyGHCR(ctx context.Context, image, version string) error {
	if image == "" {
		return rgerrors.New(rgerrors.Policy, "ghcr registry requires image")
	}
	ref := strings.TrimPrefix(image, "ghcr.io/")
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return rgerrors.New(rgerrors.Policy, "invalid ghcr image: "+image)
	}
	token, err := v.ghcrToken(ctx, ref)
	if err != nil {
		return err
	}
	endpoint := v.ghcrBase + "/v2/" + ref + "/manifests/" + url.PathEscape(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return rgerrors.Wrap(rgerrors.Transient, "GHCR request", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return rgerrors.New(rgerrors.RegistryConflict, image+":"+version+" is not public/published")
	}
	return rgerrors.New(rgerrors.Transient, resp.Status)
}

func (v *Verifier) ghcrToken(ctx context.Context, scope string) (string, error) {
	endpoint := v.ghcrBase + "/token?scope=" + url.QueryEscape("repository:"+scope+":pull")
	data, found, err := v.get(ctx, endpoint)
	if err != nil {
		return "", err
	}
	if !found {
		return "", rgerrors.New(rgerrors.RegistryConflict, "GHCR token endpoint not found")
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Token == "" {
		return "", fmt.Errorf("missing GHCR token")
	}
	return payload.Token, nil
}
