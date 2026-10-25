// edgesiteauth.go — `edge.site_auth`, the site-auth gate in front of Canvas
// paths.
//
// Present, the key is the whole truth for the gate: apply sets exactly the
// declared users and realm, so a user the file no longer names is shut out.
// Absent, the gate is left exactly as it is — on or off. There is no
// declaration that turns the gate off; `drift slice auth disable` does, and
// confirms first, because it is the one direction that exposes the site.
//
// Each password is an environment variable REFERENCE (`$NAME`), never a value:
// the file is committed, and a password in it would be too. The preflight
// resolves every reference before anything deploys and refuses, naming the
// variable and the user, when one is unset or empty. No value is ever printed.
//
// The route is `drift slice auth`'s: POST /ops/slice/auth replaces the whole
// credential set, so "set exactly these users" is one write. The api bcrypts
// each password and echoes back names only.
package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// siteAuthUser is one credential on the wire. Password is write-only: the
// api never sends a digest back.
type siteAuthUser struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

// siteAuthConfig is the body /ops/slice/auth takes and returns.
type siteAuthConfig struct {
	Enabled bool           `json:"enabled"`
	Realm   string         `json:"realm,omitempty"`
	Paths   []string       `json:"paths,omitempty"`
	Users   []siteAuthUser `json:"users,omitempty"`
}

// wholeSite is the one spelling of "every Canvas path".
const wholeSite = "*"

// normaliseSitePaths checks the paths a gate covers and returns them as the
// platform stores them. It is the platform's rule, applied here so a path that
// could never match is refused on the laptop with its position: `*` alone, or
// absolute paths each covering itself and everything beneath it, written clean,
// with no `/` alone (that reads as the home page and would mean the whole site).
// One trailing slash is dropped.
func normaliseSitePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf(`name at least one path: ["*"] gates the whole site`)
	}
	if len(paths) > 32 {
		return nil, fmt.Errorf("too many paths: at most 32")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == wholeSite {
			if len(paths) != 1 {
				return nil, fmt.Errorf(`"*" gates the whole site, so it cannot be listed beside other paths`)
			}
			return []string{wholeSite}, nil
		}
		if len(p) > 256 {
			return nil, fmt.Errorf("path %q is too long: at most 256 characters", p)
		}
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf(`path %q must start with "/" (write "*" to gate the whole site)`, p)
		}
		for _, r := range p {
			if r <= 0x20 || r == 0x7f || r == '?' || r == '#' || r == '\\' {
				return nil, fmt.Errorf("path %q contains a character a path cannot carry here", p)
			}
		}
		if p == "/" {
			return nil, fmt.Errorf(`path "/" is ambiguous: write "*" to gate the whole site, or name the pages to gate`)
		}
		norm := strings.TrimSuffix(p, "/")
		if path.Clean(norm) != norm {
			return nil, fmt.Errorf("path %q is not clean: write it without `.`, `..` or doubled slashes", p)
		}
		if seen[norm] {
			return nil, fmt.Errorf("path %q is listed twice", norm)
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out, nil
}

// samePaths reports whether two gates cover the same paths, whatever order they
// list them in. A live gate that lists none covers the whole site.
func samePaths(a, b []string) bool {
	a, b = append([]string(nil), sitePathsOrWhole(a)...), append([]string(nil), sitePathsOrWhole(b)...)
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// describeSitePaths says what a gate covers, for a line a person reads.
func describeSitePaths(paths []string) string {
	paths = sitePathsOrWhole(paths)
	if len(paths) == 1 && paths[0] == wholeSite {
		return "the whole site"
	}
	return strings.Join(paths, ", ")
}

// sitePathsOrWhole is the paths a live gate covers: a gate set before paths
// existed reports none, and covers the whole site.
func sitePathsOrWhole(paths []string) []string {
	if len(paths) == 0 {
		return []string{wholeSite}
	}
	return paths
}

// siteAuthEntry is one declared user and the variable its password is read
// from.
type siteAuthEntry struct {
	User string
	Env  string
}

// declaredSiteAuth reads `edge.site_auth` without resolving anything. It
// refuses a password that is not a `$NAME` reference, a user named twice, and
// a gate with nobody in it, and never puts a password in a message.
func declaredSiteAuth(m *Manifest) (realm string, paths []string, entries []siteAuthEntry, err error) {
	gate := m.Slice().Sub("edge", "site_auth")
	realm = gate.Str("realm")
	paths, err = normaliseSitePaths(gate.Strings("paths"))
	if err != nil {
		return "", nil, nil, fmt.Errorf("edge.site_auth: paths: %w", err)
	}
	seen := map[string]bool{}
	for _, u := range gate.Nodes("users") {
		name := u.Str("user")
		ref := u.Str("password")
		env := strings.TrimPrefix(ref, "$")
		if !strings.HasPrefix(ref, "$") || !isEnvName(env) {
			return "", nil, nil, fmt.Errorf("edge.site_auth: the password for user %q must be an environment "+
				"variable reference such as $SITE_PASS, not a value: the Driftfile is committed, and the "+
				"password would be committed with it", name)
		}
		if seen[name] {
			return "", nil, nil, fmt.Errorf("edge.site_auth: user %q is declared more than once", name)
		}
		seen[name] = true
		entries = append(entries, siteAuthEntry{User: name, Env: env})
	}
	if len(entries) == 0 {
		return "", nil, nil, fmt.Errorf("edge.site_auth declares no users, and a gate nobody can pass locks " +
			"the site to everyone. To remove the gate, delete the key and run `drift slice auth disable`")
	}
	return realm, paths, entries, nil
}

// unreadablePassword says why entry's variable cannot be used, or "" when it
// can. Names only: the value never reaches a message.
func unreadablePassword(e siteAuthEntry) string {
	val, set := os.LookupEnv(e.Env)
	state := ""
	switch {
	case !set:
		state = "not set"
	case val == "":
		state = "empty"
	default:
		return ""
	}
	return fmt.Sprintf("the password for user %q is read from $%s, which is %s", e.User, e.Env, state)
}

// desiredSiteAuth is the gate the file declares, with every password read from
// the environment. Every unreadable variable is reported at once.
func desiredSiteAuth(m *Manifest) (siteAuthConfig, error) {
	realm, paths, entries, err := declaredSiteAuth(m)
	if err != nil {
		return siteAuthConfig{}, err
	}
	cfg := siteAuthConfig{Enabled: true, Realm: realm, Paths: paths}
	var problems []string
	var fixes []string
	for _, e := range entries {
		if p := unreadablePassword(e); p != "" {
			problems = append(problems, p)
			fixes = append(fixes, e.Env)
			continue
		}
		cfg.Users = append(cfg.Users, siteAuthUser{Name: e.User, Password: os.Getenv(e.Env)})
	}
	switch len(problems) {
	case 0:
		return cfg, nil
	case 1:
		return siteAuthConfig{}, fmt.Errorf("edge.site_auth: %s. Export it, pass --secret %s=..., "+
			"or add it to the .env beside the Driftfile", problems[0], fixes[0])
	default:
		return siteAuthConfig{}, fmt.Errorf("edge.site_auth: %d passwords cannot be read:\n  - %s\n"+
			"Export them, pass --secret NAME=... for each, or add them to the .env beside the Driftfile",
			len(problems), strings.Join(problems, "\n  - "))
	}
}

// preflightEdgeSiteAuth runs before the first deploy step: a gate whose
// passwords cannot be read refuses the file before any of it is applied.
func preflightEdgeSiteAuth(m *Manifest) error {
	if !m.Slice().Has("edge", "site_auth") {
		return nil
	}
	_, err := desiredSiteAuth(m)
	return err
}

// applyEdgeSiteAuth sets the gate to exactly what `edge.site_auth` declares.
// Absent, it does nothing.
//
// The live gate is read first only to say who the write shuts out; a failed
// read costs that line and nothing else, because the write replaces the whole
// set either way.
func applyEdgeSiteAuth(m *Manifest) error {
	if !m.Slice().Has("edge", "site_auth") {
		return nil
	}
	cfg, err := desiredSiteAuth(m)
	if err != nil {
		return err
	}
	live, liveErr := fetchSiteAuth(m.Name())

	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("edge.site_auth: %w", err)
	}
	resp, err := common.DoRequestWithHeaders(http.MethodPost, common.APIBaseURL+"/ops/slice/auth",
		bytes.NewReader(body), map[string]string{"Content-Type": "application/json", "X-Slice": m.Name()})
	if err != nil {
		return fmt.Errorf("edge.site_auth: %w", common.TransportError("set the site gate", err))
	}
	defer resp.Body.Close()
	if _, err := common.CheckResponse(resp, "set the site gate"); err != nil {
		return fmt.Errorf("edge.site_auth: %w", err)
	}

	names := make([]string, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		names = append(names, u.Name)
	}
	fmt.Printf("  %s site gate set: %s can sign in; it covers %s\n", common.Check(), strings.Join(names, ", "),
		describeSitePaths(cfg.Paths))
	if liveErr == nil {
		if removed := removedSiteAuthUsers(cfg, live); len(removed) > 0 {
			fmt.Printf("    %s\n", common.Hint("removed: "+strings.Join(removed, ", ")))
		}
	}
	return nil
}

// fetchSiteAuth reads slice's live gate: realm and user names, never a digest.
func fetchSiteAuth(sliceName string) (siteAuthConfig, error) {
	resp, err := common.DoRequestWithHeaders(http.MethodGet, common.APIBaseURL+"/ops/slice/auth", nil,
		map[string]string{"X-Slice": sliceName})
	if err != nil {
		return siteAuthConfig{}, common.TransportError("read the site gate", err)
	}
	defer resp.Body.Close()
	raw, err := common.CheckResponse(resp, "read the site gate")
	if err != nil {
		return siteAuthConfig{}, err
	}
	var cfg siteAuthConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return siteAuthConfig{}, fmt.Errorf("Couldn't read the site gate: the platform sent a reply we couldn't read") //nolint:staticcheck // ST1005: user-facing copy.
	}
	return cfg, nil
}

// removedSiteAuthUsers is who the live gate lets in and the declared one does
// not, sorted.
func removedSiteAuthUsers(declared, live siteAuthConfig) []string {
	if !live.Enabled {
		return nil
	}
	keep := map[string]bool{}
	for _, u := range declared.Users {
		keep[u.Name] = true
	}
	var removed []string
	for _, u := range live.Users {
		if !keep[u.Name] {
			removed = append(removed, u.Name)
		}
	}
	sort.Strings(removed)
	return removed
}

// realmOrNone renders an empty realm as something a reader can see.
func realmOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
