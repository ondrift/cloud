package atomic_cmd

import (
	"strings"
	"testing"

	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
)

// A crate that enables the SDK's `tls` feature needs a musl C compiler, because
// `tls` pulls ring and ring is C and assembly. The stock rust image has none, so
// the build dies inside a build script with
//
//	error occurred in cc-rs: failed to find tool "aarch64-linux-musl-gcc"
//
// naming a binary the user cannot install: the compile happens in a container
// they do not control. Detecting the feature is what lets the CLI supply the
// compiler instead of asking for it, so this is the test that the escape hatch
// the SDK documents actually works.
func TestCrateWantsTLSDetectsTheFeature(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want bool
		why  string
	}{
		{
			name: "the documented one-line opt-in",
			toml: `[dependencies]
drift-sdk = { git = "https://github.com/ondrift/cloud/sdk", features = ["tls"] }`,
			want: true,
			why:  "this is the exact line the SDK's own Cargo.toml comment tells a user to write",
		},
		{
			name: "several features, tls among them",
			toml: `drift-sdk = { git = "…", features = ["tls", "something-else"] }`,
			want: true,
			why:  "the feature list is not always a single entry",
		},
		{
			name: "ureq's own tls feature, enabled directly",
			toml: `ureq = { version = "2", default-features = false, features = ["tls"] }`,
			want: true,
			why:  "ring arrives through ureq however the feature was reached, so the compiler is needed either way",
		},
		{
			name: "the scaffold as generated",
			toml: newRustManifest(t),
			want: false,
			why:  "a new function talks plain HTTP to the loopback Backbone and must not pay for musl-tools",
		},
		{
			name: "no features at all",
			toml: `[dependencies]
drift-sdk = { git = "https://github.com/ondrift/cloud/sdk" }
serde_json = "1"`,
			want: false,
			why:  "the common case keeps the stock image",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := crateWantsTLS(c.toml); got != c.want {
				t.Errorf("crateWantsTLS = %v, want %v — %s", got, c.want, c.why)
			}
		})
	}
}

// The scaffold must NOT enable tls, and must still tell the reader how. Both
// halves matter: enabling it by default makes every new Rust function pay ring's
// compile, and saying nothing leaves the user with a status-0 response body and
// no idea that "tls" is the word they need.
func TestScaffoldKeepsTLSOffButNamesIt(t *testing.T) {
	toml := newRustManifest(t)

	if crateWantsTLS(toml) {
		t.Error("the scaffold enables tls, so every new Rust function now compiles ring")
	}
	if !strings.Contains(toml, `features = ["tls"]`) {
		t.Error("the scaffold never shows the line that turns HTTPS on, so a user who hits the " +
			"status-0 body has to discover ureq's feature name on their own")
	}
}

// newRustManifest is the Cargo.toml `drift atomic new -l rust` writes. Read from
// the generator rather than restated here, so this test cannot pass against a
// copy of the scaffold that the CLI has stopped producing.
func newRustManifest(t *testing.T) string {
	t.Helper()
	name, contents := atomic_cmd_new.Manifest("rust", "probe")
	if name != "Cargo.toml" {
		t.Fatalf("the rust manifest is %q, not Cargo.toml — this test is reading the wrong file", name)
	}
	return contents
}
