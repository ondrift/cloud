package project

import (
	"io"
	"os"
	"strings"
	"testing"
)

// A literal in `backbone.secrets:` is a value in the repository, and nothing
// said so.
//
// The Driftfile is the one file guaranteed to be committed, and the field
// accepts a plain string with no constraint — so it is a legal place to write a
// live credential, and the schema and the docs both showed that form first.
// `drift file lint` printed "✓ is valid" over it.

func TestCheckHardcodedSecrets_NamesEveryLiteral(t *testing.T) {
	m := manifestFrom(Node{"slice": "demo", "backbone": map[string]any{"secrets": map[string]any{
		"STRIPE_KEY": "sk_live_abc123",
		"APP_NAME":   "Prorata",
		"RESEND_KEY": "$RESEND_KEY",
	}}})

	warnings := checkHardcodedSecrets(m)
	if len(warnings) == 0 {
		t.Fatal("two literal secrets went unreported")
	}
	joined := strings.Join(warnings, "\n")

	// Both literals, named. A warning that said "some secrets are hardcoded"
	// leaves the reader diffing the file themselves.
	for _, want := range []string{`"STRIPE_KEY"`, `"APP_NAME"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning must name %s, got:\n%s", want, joined)
		}
	}
	// And the referenced one is NOT named. Reporting it would make the warning
	// fire on every manifest that does the right thing, which is how a warning
	// becomes noise people filter out.
	if strings.Contains(joined, "RESEND_KEY") {
		t.Errorf("a $VAR reference was reported as hardcoded:\n%s", joined)
	}

	// BOTH remedies, because a literal is one of two different mistakes and the
	// fix differs. A credential belongs behind $VAR; configuration belongs in
	// `env:`, which is what keeps a new `secrets:` entry meaning "this function
	// gained privilege".
	for _, want := range []string{"$SECRET", "drift backbone secret set", "env:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning must point at %q, got:\n%s", want, joined)
		}
	}
}

// A manifest doing the right thing says nothing at all. A check that warned on
// every project would be turned off.
func TestCheckHardcodedSecrets_SaysNothingWhenEveryValueIsAReference(t *testing.T) {
	m := manifestFrom(Node{"slice": "demo", "backbone": map[string]any{"secrets": map[string]any{
		"RESEND_KEY": "$RESEND_KEY",
		"DB_URL":     "$DB_URL",
	}}})
	if w := checkHardcodedSecrets(m); len(w) != 0 {
		t.Errorf("a fully referenced manifest must be silent, got: %v", w)
	}
}

// And so does one with no secrets at all — the common case.
func TestCheckHardcodedSecrets_SaysNothingWithoutSecrets(t *testing.T) {
	m := manifestFrom(Node{"slice": "demo"})
	if w := checkHardcodedSecrets(m); len(w) != 0 {
		t.Errorf("a manifest with no secrets must be silent, got: %v", w)
	}
}

// IT WARNS AND DOES NOT REFUSE, which is the whole judgement here.
//
// Literals are legal, documented, and in use. Failing the parse would break
// working manifests to make a point — and would land on the person who wrote
// `APP_NAME: "Prorata"`, who is doing nothing dangerous.
func TestParse_AHardcodedSecretIsAWarningRatherThanARefusal(t *testing.T) {
	p := writeManifest(t, `
slice: demo
atomic:
  functions:
    - route: ping
      method: get
      handler: GetPing
backbone:
  secrets:
    STRIPE_KEY: sk_live_abc123
`)
	if _, err := ParseDriftfile(p); err != nil {
		t.Fatalf("a hardcoded secret must not fail the parse — it is legal and in use: %v", err)
	}
}

// THE ORDER IS THE CHECK.
//
// `resolveSecretEnvRefs` substitutes `$VAR` in place, so after it runs every
// value is a literal and nothing can tell the two apart. A pass that ran after
// it would report every referenced secret as hardcoded — the exact inverse of
// what it is for, and it would look like it was working.
//
// Driven through the real parse with the variable SET, so the substitution
// genuinely happens, and reading the warnings the parse actually printed.
func TestParse_AResolvedReferenceIsNotReportedAsHardcoded(t *testing.T) {
	t.Setenv("RESEND_KEY", "re_live_resolved")
	body := `
slice: demo
atomic:
  functions:
    - route: ping
      method: get
      handler: GetPing
backbone:
  secrets:
    RESEND_KEY: $RESEND_KEY
`
	p := writeManifest(t, body)

	var m *Manifest
	stderr := captureStderr(t, func() {
		var err error
		if m, err = ParseDriftfile(p); err != nil {
			t.Fatalf("parse: %v", err)
		}
	})

	// The substitution genuinely happened — without this the test would pass
	// against a parse that never resolved anything.
	if got := m.Slice().Sub("backbone", "secrets").StrMap()["RESEND_KEY"]; got != "re_live_resolved" {
		t.Fatalf("the reference was not resolved, so this proves nothing: %q", got)
	}
	// And the parse said nothing about it. A check ordered after the resolution
	// would report this — and every other correctly-written secret — as
	// hardcoded, while looking like it was working.
	if strings.Contains(stderr, "RESEND_KEY") {
		t.Errorf("a resolved $VAR was reported as hardcoded, so the check runs after "+
			"the substitution:\n%s", stderr)
	}
	// The value itself must never reach the terminal either.
	if strings.Contains(stderr, "re_live_resolved") {
		t.Errorf("the warning printed a secret's VALUE:\n%s", stderr)
	}
}

// A literal reaches the terminal by NAME and never by value. The whole point is
// that this value is somewhere it should not be; echoing it puts it somewhere
// else too — a terminal, a CI log, a screenshot.
func TestParse_AWarningNamesTheKeyAndNeverTheValue(t *testing.T) {
	p := writeManifest(t, `
slice: demo
atomic:
  functions:
    - route: ping
      method: get
      handler: GetPing
backbone:
  secrets:
    STRIPE_KEY: sk_live_do_not_print_me
`)
	stderr := captureStderr(t, func() {
		if _, err := ParseDriftfile(p); err != nil {
			t.Fatalf("parse: %v", err)
		}
	})
	if !strings.Contains(stderr, "STRIPE_KEY") {
		t.Errorf("the parse said nothing about a hardcoded secret:\n%s", stderr)
	}
	if strings.Contains(stderr, "sk_live_do_not_print_me") {
		t.Errorf("the warning printed the credential it is warning about:\n%s", stderr)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it
// wrote. The warnings are printed rather than returned, so reading them is the
// only way to test the call site rather than the function behind it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	fn()

	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}
