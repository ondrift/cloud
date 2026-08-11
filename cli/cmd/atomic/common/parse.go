// Package atomic_common finds the callables a Driftfile names.
//
// The Driftfile declares every Atomic function — its trigger, the memory it
// books, its gate, its secrets, and the `handler` that serves it. This package
// answers the one question the manifest cannot: which source file in the
// element holds that callable, and what language is it written in.
//
// Nothing here reads intent out of source. A comment above a function changes
// nothing about how it deploys; the manifest is the whole declaration.
package atomic_common

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// sentinelRe is the per-language regex matching a callable declaration line.
// Only the callable's NAME is captured (group 1).
//
// These are the shapes a handler may take. A callable the manifest names has
// to be findable from outside its own file — exported in Go, `pub` in Rust, a
// named `function` rather than an arrow bound to a const in Node — because the
// generated entry point imports it by name.
var sentinelRe = map[string]*regexp.Regexp{
	"go":     regexp.MustCompile(`^[ \t]*func[ \t]+([A-Z][A-Za-z0-9_]*)[ \t]*\(`),
	"python": regexp.MustCompile(`^[ \t]*def[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
	"node":   regexp.MustCompile(`^[ \t]*(?:async[ \t]+)?function[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
	"ruby":   regexp.MustCompile(`^[ \t]*def[ \t]+([A-Za-z_][A-Za-z0-9_?!]*)[ \t]*(?:\(|$)`),
	"php":    regexp.MustCompile(`^[ \t]*function[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
	"rust":   regexp.MustCompile(`^[ \t]*pub[ \t]+fn[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
}

// callableShape is what a findable callable looks like per language, quoted
// back at the user when the one they named is absent. Naming the shape is the
// whole point: "not found" is not actionable when the function is right there
// and merely lowercase, or bound to a const.
var callableShape = map[string]string{
	"go":     "func PostUsers(...)            // exported: capital first letter",
	"python": "def post_users(...):",
	"node":   "function postUsers(...) {      // a NAMED function, not an arrow assigned to a const",
	"ruby":   "def post_users(...)",
	"php":    "function postUsers(...) {",
	"rust":   "pub fn post_users(...)         // must be pub",
}

// languageFromExt returns the parser language key for a file extension.
func languageFromExt(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "node"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".rs":
		return "rust"
	}
	return ""
}

// LanguageFromExt reports the Drift language key for a file extension (e.g.
// ".py" → "python"), or "" if the extension isn't a supported source file.
func LanguageFromExt(ext string) string { return languageFromExt(ext) }

// Callable is where a handler lives: the file declaring it and the language
// that file is written in.
type Callable struct {
	// Handler is the callable's name, exactly as the Driftfile wrote it.
	Handler string
	// SourceFile is the basename of the file declaring it. Interpreted
	// entry points import the handler from this module.
	SourceFile string
	// Language is the Drift language key ("go", "python", "node", "ruby",
	// "php", "rust").
	Language string
}

// sourceFilesIn lists dir's top-level source files, sorted, with the language
// each maps to. Subdirectories are not descended: an element is one flat
// package, and a nested directory is that language's own vendor or module
// tree rather than more handlers.
func sourceFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if languageFromExt(filepath.Ext(e.Name())) == "" {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files, nil
}

// ElementLanguage reports the one language an element directory is written in.
//
// An element is one language by construction — it has a single dependency
// manifest and a single runtime — so a directory holding two is refused here
// rather than producing a build that silently drops half the handlers.
func ElementLanguage(dir string) (string, error) {
	files, err := sourceFilesIn(dir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no source files in %s — the Driftfile names functions here, "+
			"so this directory must hold the code that serves them", dir)
	}

	lang := languageFromExt(filepath.Ext(files[0]))
	for _, f := range files[1:] {
		if l := languageFromExt(filepath.Ext(f)); l != lang {
			return "", fmt.Errorf(
				"%s mixes languages (%s in %s, %s in %s) — an element is one language, "+
					"one dependency manifest and one runtime; move the %s functions into "+
					"their own element (a separate folder under atomic/, named by the "+
					"`element:` key on those functions)",
				dir, lang, files[0], l, f, l)
		}
	}
	return lang, nil
}

// FindCallable locates the callable named handler in dir.
//
// The search is scoped to ONE element directory, never the whole project, so
// two elements may each declare a `handler`. Within an element the name has to
// be unique: the generated entry point imports it by name, and two candidates
// mean the build would pick one and silently drop the other.
func FindCallable(dir, handler string) (Callable, error) {
	if handler == "" {
		return Callable{}, fmt.Errorf("no handler named for a function in %s", dir)
	}

	lang, err := ElementLanguage(dir)
	if err != nil {
		return Callable{}, err
	}
	rx, ok := sentinelRe[lang]
	if !ok {
		return Callable{}, fmt.Errorf("no callable pattern for language %q", lang)
	}
	files, err := sourceFilesIn(dir)
	if err != nil {
		return Callable{}, fmt.Errorf("read %s: %w", dir, err)
	}

	var found []string
	for _, fname := range files {
		names, rerr := callableNames(filepath.Join(dir, fname), rx)
		if rerr != nil {
			return Callable{}, rerr
		}
		for _, n := range names {
			if n == handler {
				found = append(found, fname)
				break
			}
		}
	}

	switch len(found) {
	case 1:
		return Callable{Handler: handler, SourceFile: found[0], Language: lang}, nil
	case 0:
		return Callable{}, fmt.Errorf(
			"%s declares a function served by %q, and no such callable is in %s.\n"+
				"       In %s a handler looks like:\n         %s\n"+
				"       %s",
			"the Driftfile", handler, dir, lang, callableShape[lang], nearMiss(dir, rx, handler, files))
	default:
		return Callable{}, fmt.Errorf(
			"%q is declared in %d files in %s (%s) — a handler has to be unique within its "+
				"element, because the entry point imports it by name and cannot say which one "+
				"you meant. Rename one, or split them into separate elements.",
			handler, len(found), dir, strings.Join(found, ", "))
	}
}

// callableNames returns every callable declared in a file.
func callableNames(path string, rx *regexp.Regexp) ([]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the CLI reads the user's own source by design
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if m := rx.FindStringSubmatch(line); len(m) >= 2 {
			names = append(names, m[1])
		}
	}
	return names, nil
}

// nearMiss adds the one sentence that turns "not found" into a fix.
//
// The overwhelmingly common cause is a handler that IS there and merely spelled
// differently — a lowercase Go function, a `handle_order` written `handleOrder`
// in the manifest. Listing what the directory does declare costs one pass over
// files already read and answers the question the bare error leaves open.
func nearMiss(dir string, rx *regexp.Regexp, handler string, files []string) string {
	var all []string
	for _, f := range files {
		names, err := callableNames(filepath.Join(dir, f), rx)
		if err != nil {
			continue
		}
		all = append(all, names...)
	}
	if len(all) == 0 {
		return "Nothing in that directory declares a callable of any name."
	}
	for _, n := range all {
		if strings.EqualFold(n, handler) {
			return fmt.Sprintf("There IS a %q — the names differ only in case, and the match is exact.", n)
		}
	}
	sort.Strings(all)
	return "Callables found there: " + strings.Join(all, ", ") + "."
}

// CallableExists reports whether dir declares a callable of this name, without
// building the error prose. Used where absence is a question rather than a
// failure.
func CallableExists(dir, handler string) bool {
	_, err := FindCallable(dir, handler)
	return err == nil
}
