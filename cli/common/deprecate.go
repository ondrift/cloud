package common

// deprecate.go — retiring a user-facing name without breaking the people using it.
//
// The rule this implements: a command, flag or spelling that goes away is first
// DEPRECATED, keeps working, and is only then removed. The old spelling does
// exactly what it always did and says once, on stderr, what to type instead.
//
// # Why not cobra's own `Deprecated` field
//
// Three reasons, each of which bit something real:
//
//   - It prints through `Command.Print`, which is `OutOrStderr()` — the command's
//     OUTPUT writer, falling back to stderr only when none is set. A warning is
//     not output, and anything parsing stdout should never have to filter it.
//     This writes to stderr and takes no opinion from the command tree.
//   - It renders `c.Name()`, the leaf. For `drift project deploy` that reads
//     `Command "deploy" is deprecated`, which does not say what to stop typing.
//     A deprecation notice that omits the path is a riddle.
//   - It carries no removal condition, so a shim has no stated exit and quietly
//     becomes a permanent second surface.
//
// # The shape a deprecation must have
//
// A `Deprecation` is DATA, not behaviour: the old spelling, the new one, and when
// it goes. That is what makes the set of them auditable — `drift` can be asked
// what is currently deprecated without running any of it.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

// Deprecation is one retired user-facing name.
//
// Old and New are what a person TYPES, in full ("drift project deploy"), not the
// leaf. RemoveAfter is the condition that ends the shim's life, written where the
// shim lives so it is a stage rather than a permanent second surface — a version
// ("v1.0.0") or an event ("the closed alpha ends").
type Deprecation struct {
	Old         string
	New         string
	RemoveAfter string
	// Because is an optional half-sentence for a rename whose reason is not
	// obvious from the two names. Most renames need none.
	Because string
}

// deprecationWarnings is where the notice goes. A variable so a test can read
// what was written; os.Stderr rather than the command's writer because a warning
// is not output (see the header).
var deprecationWarnings io.Writer = os.Stderr

// warnedOnce keeps one notice per process per old name.
//
// Cobra can enter a command more than once in a single run — and an alias
// forwards into the real command, which is a second Execute on the same tree —
// so without this a user renaming one thing sees the same line twice and starts
// reading it as noise.
var warnedOnce sync.Map

// registry is every deprecation this binary declares, so the set can be listed
// without invoking any of them.
var (
	registryMu sync.Mutex
	registry   []Deprecation
)

// Warn prints the notice once for this old name, and records the deprecation.
//
// Safe to call on every invocation: the second call for the same Old is a no-op.
func (d Deprecation) Warn() {
	registryMu.Lock()
	found := false
	for _, existing := range registry {
		if existing.Old == d.Old {
			found = true
			break
		}
	}
	if !found {
		registry = append(registry, d)
	}
	registryMu.Unlock()

	if _, already := warnedOnce.LoadOrStore(d.Old, true); already {
		return
	}
	fmt.Fprintln(deprecationWarnings, d.Notice())
}

// Notice is the line a user sees. Separate from Warn so a test can assert the
// wording without capturing a stream, and so `drift` can render the same
// sentence when listing deprecations.
func (d Deprecation) Notice() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%q is deprecated", d.Old))
	if d.RemoveAfter != "" {
		b.WriteString(fmt.Sprintf(" and will be removed after %s", d.RemoveAfter))
	}
	b.WriteString(".")
	if d.New != "" {
		b.WriteString(fmt.Sprintf(" Use %q instead.", d.New))
	}
	if d.Because != "" {
		b.WriteString(" " + d.Because)
	}
	return b.String()
}

// Deprecations returns every deprecation this binary has declared, sorted by the
// old spelling. Sorted rather than in registration order so the list reads the
// same however the command tree is assembled.
func Deprecations() []Deprecation {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Deprecation, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].Old < out[j].Old })
	return out
}

// DeprecateCommand marks one command as deprecated in place: it keeps its own
// implementation and gains the notice.
//
// For a command that MOVED, use AliasCommand instead — that keeps one
// implementation, which this cannot.
func DeprecateCommand(cmd *cobra.Command, d Deprecation) *cobra.Command {
	inner := cmd.PersistentPreRunE
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		d.Warn()
		if inner != nil {
			return inner(c, args)
		}
		return nil
	}
	return cmd
}

// AliasCommand returns a hidden command under the OLD name that re-runs the CLI
// under the new one.
//
// It parses nothing and decides nothing: `DisableFlagParsing` hands it the raw
// tail, it swaps the first word, and it re-enters the root. So every subcommand,
// flag and argument reaches the real implementation exactly as typed, and there
// is one implementation to fix (Article III).
//
// # Two shapes that look like they would work, and do not
//
//   - **Copying the command.** The copy's children keep the ORIGINAL as their
//     parent, so cobra's pre-run walk goes straight past the copy's hook: bare
//     `drift project` warns and `drift project deploy` says nothing. The verb is
//     what people type, so the silent case is the one that matters.
//   - **cobra's own `Aliases` plus `CalledAs()`.** `CalledAs` reports only for
//     the FINAL resolved command — cobra sets its `called` flag for that one
//     alone — so a group reached on the way to a subcommand reports `""` and the
//     notice never fires. Same failure, one level deeper.
//
// Re-entering the root is what avoids both: the second pass resolves the new
// spelling normally, with the whole tree intact. `Warn` is once per process, so
// the notice does not double up on the way through.
func AliasCommand(target *cobra.Command, oldName string, d Deprecation) *cobra.Command {
	return &cobra.Command{
		Use:    oldName,
		Short:  fmt.Sprintf("Deprecated: use %q", d.New),
		Hidden: true,
		// The tail belongs to the real command, so this must not try to read it:
		// an unknown flag here would fail before the notice is ever printed.
		DisableFlagParsing: true,
		RunE: func(c *cobra.Command, args []string) error {
			d.Warn()
			root := c.Root()
			// The target's FULL path, not just its name: a nested alias
			// (`drift file deploy` -> `drift file apply`) has to re-enter as
			// "file apply", and prepending the leaf alone would send it to
			// "drift apply", which does not exist.
			path := strings.Fields(target.CommandPath())
			root.SetArgs(append(path[1:], args...)) // drop the root's own name
			return root.Execute()
		},
	}
}

// ---------- key-level deprecations ----------
//
// A command is not the only user-facing name that gets retired. A Driftfile key
// is one too, and JSON Schema draft-07 cannot express "deprecated but working":
// with `additionalProperties: false` a key is legal or it is an error, so a key
// removed from `properties` breaks every existing document on the next api
// deploy, with no notice. The standing rule is deprecate, alias, then remove —
// which means the key stays in the schema and the CLIENT says something about it.
//
// The data and the printing are the ones above: KeyDeprecation carries a
// Deprecation, so the registry, the once-per-Old rule and the wording are shared
// rather than reimplemented for documents.

// KeyKind is what a retired key does now.
type KeyKind int

const (
	// KeyAlias has a real target. The walker REWRITES the node so exactly one
	// spelling reaches every downstream reader — which is what makes the old name
	// a thin alias rather than a second implementation. No reader learns that two
	// spellings ever existed.
	KeyAlias KeyKind = iota
	// KeyIgnored has no target. Every capacity key is one of these: the value
	// moved to a different owner entirely, so there is nothing to rewrite it to.
	// The walker warns and leaves the value alone; nothing downstream reads it and
	// the schema still accepts it.
	KeyIgnored
)

// KeyDeprecation is one retired Driftfile key.
//
// Path addresses the key in the document, with `[]` meaning "every element of
// this list" — `atomic.functions[].memory`. Four of the five renames in the
// shape split live inside lists, so addressing elements is not a nicety.
//
// The path is also the notice's identity: Warn keys on Deprecation.Old, so one
// PATH yields one notice however many times it occurs. Prorata declares
// `memory` nineteen times and hears about it once.
type KeyDeprecation struct {
	Path string
	Kind KeyKind
	Deprecation
}

// KeyDeprecationFor builds the entry, defaulting the notice's Old to the path so
// the two cannot drift apart.
func KeyDeprecationFor(path string, kind KeyKind, d Deprecation) KeyDeprecation {
	if d.Old == "" {
		d.Old = path
	}
	return KeyDeprecation{Path: path, Kind: kind, Deprecation: d}
}

// RedirectDeprecationWarnings sends notices somewhere a test can read, and
// returns the function that puts them back.
//
// Exported because the walker that emits key-level notices lives in cmd/project,
// which cannot reach the unexported writer. The writer exists for exactly this —
// a notice is not output, so it must be assertable without capturing the
// command's own stream.
func RedirectDeprecationWarnings(w io.Writer) (restore func()) {
	prev := deprecationWarnings
	deprecationWarnings = w
	return func() { deprecationWarnings = prev }
}

// ResetDeprecationState clears the once-per-name record and the registry.
//
// For tests only, and it exists because both are deliberately process-wide: the
// once-rule is what stops a person seeing the same line twice in one run, and a
// test asserting that rule has to be able to start from nothing.
func ResetDeprecationState() {
	warnedOnce = sync.Map{}
	registryMu.Lock()
	registry = nil
	registryMu.Unlock()
}
