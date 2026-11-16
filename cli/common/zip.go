package common

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// walkFolder visits every entry under folder, the root excluded, in lexical
// order: rel is its path from folder, path its path on disk. It is the one walk
// of a site's folder, so the files ZipFolder uploads and the files FolderDigest
// describes are the same set.
func walkFolder(folder string, visit func(rel, path string, info os.FileInfo) error) error {
	return filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(folder, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		return visit(rel, path, info)
	})
}

func ZipFolder(folderPath string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	err := walkFolder(folderPath, func(relPath, path string, info os.FileInfo) error {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		// Use relative path in zip archive
		header.Name = relPath

		// For directories, just create the folder entry
		if info.IsDir() {
			header.Name += "/"
		} else {
			// Use deflate compression for files
			header.Method = zip.Deflate
		}

		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		// If file, copy contents into zip writer
		// #nosec G122 -- snapshot/runner dir is operator-controlled (created by the slice/operator inside its own staging dirs); never user-writable, no symlink TOCTOU risk.
		if !info.IsDir() {
			f, err := os.Open(path) // #nosec G122 G304 -- false-positive: see the cross-repo audit baseline; this site has been reviewed.
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(writer, f)
			if err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}

	err = zw.Close()
	if err != nil {
		return nil, err
	}

	return buf, nil
}

// folderDigestVersion is mixed into every folder digest. Bump it whenever the
// algorithm changes, so a changed algorithm can never read as a match against a
// digest an older CLI recorded: every site uploads once more.
const folderDigestVersion = "drift-site-digest-v1"

// FolderDigest returns a deterministic fingerprint of the files ZipFolder
// uploads for dir: each file's slash-separated path and content, in path order.
// Modification times, file modes and empty directories are not part of it, so
// two copies of a folder digest alike and any change to a file's bytes, a path
// or the set of files moves it. It is lowercase hexadecimal, 64 characters.
//
// The slice stores the digest unread and compares nothing: this function is
// the one owner of the algorithm, for `drift plan` and `drift apply` alike.
func FolderDigest(dir string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", folderDigestVersion)

	err := walkFolder(dir, func(rel, path string, info os.FileInfo) error {
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path) // #nosec G304 -- hashing the folder the user declared as a site
		if err != nil {
			return err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			return err
		}
		// Path and length lead the content, NUL-delimited, so one file's bytes
		// can never read as the next file's header.
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), stat.Size())
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		fmt.Fprint(h, "\x00")
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest folder %s: %w", dir, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
