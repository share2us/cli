package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// Windows releases ship as .zip, so extractBinaryFromArchive must pick the zip
// reader off the extension and find the binary by base name.
func TestExtractBinaryFromArchiveZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "share2us_windows_amd64.zip")

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range map[string]string{
		"README.md":    "not the binary",
		"share2us.exe": "windows-binary",
		"nested/other": "decoy",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	got, err := extractBinaryFromArchive(archivePath, dest, "share2us.exe")
	if err != nil {
		t.Fatalf("extractBinaryFromArchive() error = %v", err)
	}
	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "windows-binary" {
		t.Fatalf("extracted content = %q, want %q", content, "windows-binary")
	}

	if _, err := extractBinaryFromArchive(archivePath, dest, "missing.exe"); err == nil {
		t.Error("extractBinaryFromArchive() with an absent binary should fail")
	}
}
