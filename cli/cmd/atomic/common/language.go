package atomic_common

import (
	"fmt"
	"path/filepath"
)

// BuildLanguage is the language key the BUILD and the operator speak. It is the
// parser's key unchanged, and this function exists to say that in one place.
//
// It used to rewrite "go" to "native", a name from when Go was the only compiled
// language Drift accepted. Rust is compiled too and always travelled as "rust",
// so "native" named Go and nothing else while claiming to name a category.
//
// A slice understands both labels and always will — see is_compiled_go in the
// runner — because a function keeps the label it was deployed under in its
// metadata.json, and released CLI binaries go on sending "native" indefinitely.
func BuildLanguage(parserLanguage string) string {
	return parserLanguage
}

// IsGo reports whether a language label means compiled Go, accepting both the
// current "go" and the historical "native".
//
// Every place that READS a label needs this: the api hands back whatever was
// stored at deploy time, so a slice deployed last month answers "native" and one
// deployed today answers "go". Matching one alone makes a function's language
// render as unknown, or worse, silently skips the branch that places its binary.
func IsGo(language string) bool {
	return language == "go" || language == "native"
}

// DetectLanguage reports the language of a source directory and the file that
// would represent it, for callers that only need to know what a folder is
// written in — a snapshot being laid out, a project being explained.
//
// A caller that needs to build something wants FindCallable instead: a
// function's source file is the one declaring its handler, and an element
// commonly spreads its handlers across several files.
func DetectLanguage(dir string) (string, string, error) {
	lang, err := ElementLanguage(dir)
	if err != nil {
		return "", "", err
	}
	files, err := sourceFilesIn(dir)
	if err != nil {
		return "", "", err
	}
	if len(files) == 0 {
		return "", "", fmt.Errorf("no source files in %s", dir)
	}
	return BuildLanguage(lang), files[0], nil
}

// LanguageOfFile reports the build language of a single source file.
func LanguageOfFile(path string) string {
	return BuildLanguage(languageFromExt(filepath.Ext(path)))
}
