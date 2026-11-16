package common

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZipFolder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hello')"), 0o644)
	os.WriteFile(filepath.Join(dir, "drift.py"), []byte("# sdk"), 0o644)

	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatalf("ZipFolder: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty zip")
	}

	// Verify contents.
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	names := make(map[string]bool)
	for _, f := range r.File {
		names[f.Name] = true
	}
	if !names["app.py"] {
		t.Fatal("zip should contain app.py")
	}
	if !names["drift.py"] {
		t.Fatal("zip should contain drift.py")
	}
}

func TestZipFolderWithSubdirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src")
	os.MkdirAll(sub, 0o750)
	os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0o644)

	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatalf("ZipFolder: %v", err)
	}

	r, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	found := false
	for _, f := range r.File {
		if f.Name == "src/main.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("zip should contain src/main.go")
	}
}

func TestZipFolderEmpty(t *testing.T) {
	dir := t.TempDir()
	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatalf("ZipFolder empty: %v", err)
	}
	// Should still be a valid (empty) zip.
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open empty zip: %v", err)
	}
	if len(r.File) != 0 {
		t.Fatalf("expected 0 files, got %d", len(r.File))
	}
}

func TestZipFolderNonexistent(t *testing.T) {
	_, err := ZipFolder("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent folder")
	}
}

func TestZipFolderPreservesContent(t *testing.T) {
	dir := t.TempDir()
	content := "hello world 🚀"
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0o644)

	buf, _ := ZipFolder(dir)
	r, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))

	for _, f := range r.File {
		if f.Name == "test.txt" {
			rc, _ := f.Open()
			data := make([]byte, 100)
			n, _ := rc.Read(data)
			rc.Close()
			if string(data[:n]) != content {
				t.Fatalf("content: got %q, want %q", data[:n], content)
			}
			return
		}
	}
	t.Fatal("test.txt not found in zip")
}

// layFolder writes files (path -> content) under dir.
func layFolder(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustFolderDigest(t *testing.T, dir string) string {
	t.Helper()
	d, err := FolderDigest(dir)
	if err != nil {
		t.Fatalf("FolderDigest: %v", err)
	}
	return d
}

// Two copies of one folder digest alike whatever their files' modification
// times are, and the digest is a hexadecimal string the slice accepts (1 to 128
// characters).
func TestFolderDigestIgnoresModificationTimes(t *testing.T) {
	files := map[string]string{"index.html": "<h1>hi</h1>", "css/site.css": "body{}"}
	a, b := t.TempDir(), t.TempDir()
	layFolder(t, a, files)
	layFolder(t, b, files)
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for rel := range files {
		if err := os.Chtimes(filepath.Join(b, filepath.FromSlash(rel)), old, old); err != nil {
			t.Fatal(err)
		}
	}

	da, db := mustFolderDigest(t, a), mustFolderDigest(t, b)

	if da != db {
		t.Errorf("copies with different modification times digest %q and %q", da, db)
	}
	if len(da) < 1 || len(da) > 128 || strings.Trim(da, "0123456789abcdef") != "" {
		t.Errorf("the digest %q is not 1 to 128 lowercase hexadecimal characters", da)
	}
}

// One byte of one file, one file's path, or the set of files moves the digest.
func TestFolderDigestMovesWithTheFiles(t *testing.T) {
	base := map[string]string{"index.html": "<h1>hi</h1>", "css/site.css": "body{}"}
	baseDir := t.TempDir()
	layFolder(t, baseDir, base)
	want := mustFolderDigest(t, baseDir)

	cases := map[string]map[string]string{
		"one byte of one file":             {"index.html": "<h1>hI</h1>", "css/site.css": "body{}"},
		"one file's path":                  {"index.html": "<h1>hi</h1>", "css/main.css": "body{}"},
		"a file moved into another folder": {"index.html": "<h1>hi</h1>", "site.css": "body{}"},
		"an added file":                    {"index.html": "<h1>hi</h1>", "css/site.css": "body{}", "about.html": ""},
		"a removed file":                   {"index.html": "<h1>hi</h1>"},
		// A file's content and the next file's path must not blur into each other.
		"content that looks like the next entry": {"index.html": "<h1>hi</h1>", "css/site.css": "body{}\x00x\x00y"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			layFolder(t, dir, files)
			if got := mustFolderDigest(t, dir); got == want {
				t.Errorf("the digest did not move for %s", name)
			}
		})
	}
}

// Empty directories and file modes are not what a site serves, so they do not
// move the digest.
func TestFolderDigestIgnoresEmptyDirectoriesAndModes(t *testing.T) {
	files := map[string]string{"index.html": "<h1>hi</h1>"}
	plain, other := t.TempDir(), t.TempDir()
	layFolder(t, plain, files)
	layFolder(t, other, files)
	if err := os.MkdirAll(filepath.Join(other, "empty", "deeper"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(other, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}

	if a, b := mustFolderDigest(t, plain), mustFolderDigest(t, other); a != b {
		t.Errorf("an empty directory and a file mode moved the digest: %q vs %q", a, b)
	}
}

// The digest describes the files ZipFolder uploads: every file in the zip, a
// hidden one and one in a folder named like a build directory included, moves it.
func TestFolderDigestCoversEveryFileTheZipHolds(t *testing.T) {
	files := map[string]string{
		"index.html":        "a",
		".well-known/x":     "b",
		".hidden":           "c",
		"node_modules/m.js": "d",
		"dist/app.js":       "e",
	}
	dir := t.TempDir()
	layFolder(t, dir, files)
	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		seen++
		before := mustFolderDigest(t, dir)
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		if mustFolderDigest(t, dir) == before {
			t.Errorf("changing %s, which the zip uploads, did not move the digest", f.Name)
		}
	}
	if seen != len(files) {
		t.Fatalf("the zip holds %d files, the test laid %d", seen, len(files))
	}
}

func TestFolderDigestOfAFolderThatIsNotThereIsAnError(t *testing.T) {
	if _, err := FolderDigest("/nonexistent/path"); err == nil {
		t.Fatal("expected an error for a nonexistent folder")
	}
}
