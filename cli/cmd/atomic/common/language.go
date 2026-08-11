package atomic_common

import (
	"fmt"
	"path/filepath"
)

// BuildLanguage is the language key the BUILD and the operator speak, which is
// not quite the one the parser uses: Go compiles to a binary the slice runs
// directly, so it ships as "native".
func BuildLanguage(parserLanguage string) string {
	if parserLanguage == "go" {
		return "native"
	}
	return parserLanguage
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
