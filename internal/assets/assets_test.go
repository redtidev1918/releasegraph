package assets

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

func TestRequiredZipAssetGate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app-1.0.0-windows-amd64.zip")
	archive, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	file, err := writer.Create("app.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{Kind: "binary", Checksums: true, Assets: policy.Assets{Required: []string{"app-*.zip"}}}
	gate, err := Evaluate(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(gate.Required) != 1 || gate.Required[0].SHA256 == "" || gate.Required[0].Size == 0 {
		t.Fatalf("gate=%+v", gate)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("asset gate left the ZIP open: %v", err)
	}
}

func TestMissingRequiredAssetFails(t *testing.T) {
	p := &policy.Policy{Kind: "binary", Checksums: true, Assets: policy.Assets{Required: []string{"missing"}}}
	gate, err := Evaluate(p, t.TempDir())
	if err == nil || gate == nil || len(gate.Missing) != 1 {
		t.Fatalf("gate=%+v err=%v", gate, err)
	}
}
