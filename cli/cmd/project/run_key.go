package project

// run_key.go: the Backbone key `drift file run --persist` keeps beside a project.
//
// The runner image encrypts what Backbone stores with the key in
// BACKBONE_ENCRYPTION_KEY, 64 hexadecimal characters, and makes a random one at
// every start when the variable is absent. A volume kept across runs is then
// unreadable at the next run unless every run hands the container the same key.
//
// So the key is made once, the first time a project runs with --persist, and
// kept in a directory beside the Driftfile that only its owner can enter. The
// directory ignores itself, so a `git add .` cannot take the key. The key is
// never in the Driftfile and nothing prints it; an error names the file and
// never what it holds.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// backboneKeyEnv is the variable the runner image reads its key from.
	backboneKeyEnv = "BACKBONE_ENCRYPTION_KEY"

	// projectStateDir is the directory beside the Driftfile that holds what a
	// project keeps on this machine.
	projectStateDir = ".drift"

	// backboneKeyFile is the key's file inside projectStateDir.
	backboneKeyFile = "backbone.key"
)

// persistedBackboneKey returns the key of the project whose Driftfile is in dir,
// making and keeping it on first use. It never replaces a key file that exists:
// a file that is not a key is refused as it stands, because a new key would not
// open what the old one encrypted.
func persistedBackboneKey(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("the project has no directory to keep the Backbone key beside")
	}
	state := filepath.Join(dir, projectStateDir)
	path := filepath.Join(state, backboneKeyFile)

	key, err := readBackboneKey(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}

	if err := os.MkdirAll(state, 0o700); err != nil {
		return "", fmt.Errorf("keep the Backbone key beside the project: %w", err)
	}
	// Before the key exists, so there is no moment at which it is not ignored.
	if err := ignoreEverythingIn(state); err != nil {
		return "", fmt.Errorf("keep the Backbone key beside the project: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("make a Backbone key: %w", err)
	}
	key = hex.EncodeToString(raw)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- inside the project's own state directory
	if errors.Is(err, fs.ErrExist) {
		// A run of the same project made it between the read and now.
		return readBackboneKey(path)
	}
	if err != nil {
		return "", fmt.Errorf("keep the Backbone key beside the project: %w", err)
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()       // #nosec G104 -- the write already failed
		os.Remove(path) // #nosec G104 -- a partial key must not stay behind
		return "", fmt.Errorf("keep the Backbone key beside the project: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path) // #nosec G104 -- a partial key must not stay behind
		return "", fmt.Errorf("keep the Backbone key beside the project: %w", err)
	}
	return key, nil
}

// withoutKey is text with the key an argument list hands to docker taken out. It
// is for what a docker call said, which an error passes on: the CLI's own words
// never carry the key, and this keeps docker's from doing so.
func withoutKey(text string, args []string) string {
	for _, a := range args {
		if key, ok := strings.CutPrefix(a, backboneKeyEnv+"="); ok && key != "" {
			text = strings.ReplaceAll(text, key, "<key>")
		}
	}
	return text
}

// readBackboneKey reads the key at path. A missing file comes back as the error
// from os.ReadFile, so a caller can tell it from a file that is not a key.
func readBackboneKey(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- inside the project's own state directory
	if err != nil {
		return "", fmt.Errorf("read the Backbone key: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if b, err := hex.DecodeString(key); err != nil || len(b) != 32 {
		return "", fmt.Errorf("%s is not 64 hexadecimal characters and is left as it is, "+
			"because a new key cannot open what the volume holds; restore the key, "+
			"or remove this file together with the volume (drift file stop --purge)", path)
	}
	return key, nil
}

// ignoreEverythingIn writes a .gitignore into dir that ignores every file in it,
// itself included, unless dir already has one.
func ignoreEverythingIn(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, ".gitignore"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- inside the project's own state directory
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString("*\n"); err != nil {
		f.Close() // #nosec G104 -- the write already failed
		return err
	}
	return f.Close()
}
