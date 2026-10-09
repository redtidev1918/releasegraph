package assets

import (
	"archive/tar"
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

type Asset struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Gate struct {
	Required []Asset  `json:"required"`
	Optional []Asset  `json:"optional"`
	Missing  []string `json:"missing"`
}

func Evaluate(p *policy.Policy, root string) (*Gate, error) {
	required := []Asset{}
	missing := []string{}
	for _, pattern := range p.Assets.Required {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, rgerrors.Wrap(rgerrors.Asset, "invalid asset pattern "+pattern, err)
		}
		files := []string{}
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && !info.IsDir() {
				files = append(files, match)
			}
		}
		if len(files) == 0 {
			missing = append(missing, pattern)
			continue
		}
		for _, path := range files {
			asset, err := inspect(path)
			if err != nil {
				return nil, err
			}
			required = append(required, asset)
		}
	}
	optional := []Asset{}
	for _, pattern := range p.Assets.Optional {
		matches, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, path := range matches {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				asset, err := inspect(path)
				if err != nil {
					return nil, err
				}
				optional = append(optional, asset)
			}
		}
	}
	if len(missing) > 0 {
		return &Gate{Required: required, Optional: optional, Missing: missing}, rgerrors.New(rgerrors.Asset, "missing required assets: "+strings.Join(missing, ", "))
	}
	return &Gate{Required: required, Optional: optional, Missing: missing}, nil
}

func inspect(path string) (Asset, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Asset{}, rgerrors.Wrap(rgerrors.Asset, "stat asset "+path, err)
	}
	if info.Size() == 0 {
		return Asset{}, rgerrors.New(rgerrors.Asset, "empty asset: "+path)
	}
	if strings.HasSuffix(path, ".zip") {
		archive, err := zip.OpenReader(path)
		if err != nil {
			return Asset{}, rgerrors.Wrap(rgerrors.Asset, "invalid zip asset: "+path, err)
		}
		if err := archive.Close(); err != nil {
			return Asset{}, rgerrors.Wrap(rgerrors.Asset, "close zip asset "+path, err)
		}
	}
	if strings.HasSuffix(path, ".tar") || strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".tgz") || strings.HasSuffix(path, ".tar.xz") {
		if err := checkTar(path); err != nil {
			return Asset{}, err
		}
	}
	hash, err := Sum(path)
	if err != nil {
		return Asset{}, err
	}
	return Asset{Name: filepath.Base(path), Path: path, Size: info.Size(), SHA256: hash}, nil
}

func checkTar(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return rgerrors.Wrap(rgerrors.Asset, "open tar asset "+path, err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	if _, err := reader.Next(); err != nil {
		return rgerrors.New(rgerrors.Asset, "invalid tar asset: "+path)
	}
	return nil
}

func Sum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", rgerrors.Wrap(rgerrors.Asset, "hash asset "+path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", rgerrors.Wrap(rgerrors.Asset, "hash asset "+path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func Sorted(assets []Asset) []Asset {
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets
}
