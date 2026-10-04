package file

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An environment that restates its functions in the route/method spelling must
// still list each one by its identity.
//
// The overlay's list REPLACES the base's, and the parse only normalised the
// base, so every function in it reached the printer with no composite name and
// `drift file resolve --env staging` listed a bullet with nothing beside it.
// The same empty identity reached the deploy, which books and names functions
// by it.
func TestResolve_ListsAnOverlaysFunctionsByIdentity(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"slice: demo",
		"atomic:",
		"  functions:",
		"    - route: ping",
		"      method: get",
		"      handler: GetPing",
		"environments:",
		"  prod: {}",
		"  staging:",
		"    atomic:",
		"      functions:",
		"        - route: pong",
		"          method: post",
		"          handler: PostPong",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ env, want string }{
		{"staging", "· post:pong"},
		{"prod", "· get:ping"},
	} {
		var out bytes.Buffer
		restore := redirectStdout(t, &out)
		cmd := getResolveCmd()
		cmd.SetArgs([]string{dir, "--env", c.env})
		err := cmd.Execute()
		restore()
		if err != nil {
			t.Fatalf("resolve --env %s: %v", c.env, err)
		}
		if !strings.Contains(out.String(), c.want) {
			t.Errorf("resolve --env %s does not list %q:\n%s", c.env, c.want, out.String())
		}
	}
}

// The rate limit, the inbound allowlist and the fronted hosts are each printed
// as the file resolves them, `atomic.rate_limit` under its new name.
func TestResolve_PrintsTheEdgeRateLimitAllowlistAndHosts(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"slice: demo",
		"atomic:",
		"  functions:",
		"    - route: ping",
		"      method: get",
		"      handler: GetPing",
		"  rate_limit: 600/min",
		"edge:",
		"  allow: [\"203.0.113.0/24\", \"2001:db8::/32\"]",
		"  hosts:",
		"    - host: admin.example.com",
		"      slice: admin",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	restore := redirectStdout(t, &out)
	cmd := getResolveCmd()
	cmd.SetArgs([]string{dir})
	err := cmd.Execute()
	restore()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, want := range []string{
		"rate limit         600/min",
		"inbound allowlist (2)",
		"· 203.0.113.0/24",
		"· 2001:db8::/32",
		"fronted hosts (1)",
		"· admin.example.com → admin",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("resolve does not print %q:\n%s", want, out.String())
		}
	}
}

// An empty allowlist is a declaration, and resolves to a slice every address
// reaches.
func TestResolve_SaysAnEmptyAllowlistAdmitsEveryone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte("slice: demo\nedge:\n  allow: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	restore := redirectStdout(t, &out)
	cmd := getResolveCmd()
	cmd.SetArgs([]string{dir})
	err := cmd.Execute()
	restore()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(out.String(), "inbound allowlist (0)") ||
		!strings.Contains(out.String(), "every address reaches the slice") {
		t.Errorf("resolve does not say an empty allowlist admits everyone:\n%s", out.String())
	}
}

// redirectStdout captures what the command prints, which goes to os.Stdout
// rather than to the command's own writer.
func redirectStdout(t *testing.T, into *bytes.Buffer) (restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan struct{})
	go func() {
		_, _ = into.ReadFrom(r)
		close(done)
	}()

	return func() {
		os.Stdout = prev
		_ = w.Close()
		<-done
		_ = r.Close()
	}
}
