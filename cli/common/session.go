package common

// session.go — what `~/.drift/session.json` holds, and which account a command
// acts as.
//
// # One file, several accounts
//
// The file used to be flat — one token, one refresh token, one active slice —
// so logging in as a second account overwrote the first. Two things made that
// worth changing, and the second is the one that matters:
//
//   - An agent or a colleague logging in to try something clobbered the
//     operator's own session. That is real, and it is the weaker argument: an
//     isolated HOME already works around it.
//   - `active_slice` LIVES IN THIS FILE AND DOES NOT BELONG TO THE TOKEN. So
//     logging in as a second account left the PREVIOUS account's active slice
//     selected, pointing at a slice the new account may not even own. The flat
//     design already had the bug that per-account profiles fix.
//
// # Which account a command acts as
//
//	DRIFT_TOKEN set        → no account at all; see pat.go. It replaces this
//	                         file entirely and is checked before any of it.
//	DRIFT_ACCOUNT=<name>   → that profile, for this command only. Changes no
//	                         state, which is what makes it safe to script.
//	otherwise              → the `current` profile.
//
// # Migration is silent and lossless
//
// A flat file is read as a single profile named from its own token's `username`
// claim, and rewritten in the new shape on the next write. Nobody is logged out
// and nobody has to log in again. `migrate` below is the only place that knows
// the old shape.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const SessionFile = "~/.drift/session.json"

// AccountEnv names the profile to act as, for one command.
//
// Deliberately a per-command override that writes nothing: the failure this
// guards against is acting on the wrong account, and a switch that persists is
// a switch someone forgets they made.
const AccountEnv = "DRIFT_ACCOUNT"

// APIBaseURL is the base URL for the Drift API gateway. It defaults to the
// public production gateway so a plain `go install` works out of the box; a
// local/dev build points it elsewhere via:
//
//	go build -ldflags "-X github.com/ondrift/cloud/cli/common.APIBaseURL=http://api.localhost:30036"
//
// At runtime, the DRIFT_API_URL environment variable takes precedence over
// the compiled-in default (useful for self-hosted instances or staging).
var APIBaseURL = "https://api.ondrift.eu"

func init() {
	if u := os.Getenv("DRIFT_API_URL"); u != "" {
		APIBaseURL = u
	}
}

type Session struct {
	Username    string `json:"username"`
	ActiveSlice string `json:"active_slice,omitempty"`
}

// AccountProfile is one logged-in account: its credentials and the slice it is
// pointed at. The active slice is per-account BY CONSTRUCTION here, which is the
// whole point of the shape.
type AccountProfile struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ActiveSlice  string `json:"active_slice,omitempty"`
}

// sessionFile is the on-disk shape.
type sessionFile struct {
	Current  string                    `json:"current"`
	Accounts map[string]AccountProfile `json:"accounts"`
}

func expandPath(path string) (string, error) {
	// `len(path) >= 2` is load-bearing: the previous `len > 0` check
	// panicked with index-out-of-range when path was exactly "~"
	// (path[:2] over a 1-char string). The constant `SessionFile`
	// never trips this in practice, but a public-source CLI should
	// handle every input shape without panicking.
	if len(path) >= 2 && path[:2] == "~/" {
		// Use $HOME directly so that tests (and tools that override HOME) work
		// correctly. CGo's user.Current() ignores the HOME env var on macOS.
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("HOME environment variable is not set")
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// readSession loads the file, migrating a flat one on the way.
func readSession() (sessionFile, error) {
	raw, err := readSessionMap()
	if err != nil {
		return sessionFile{}, err
	}
	return migrate(raw), nil
}

// migrate turns whatever is on disk into the current shape.
//
// THE OLD SHAPE IS DETECTED BY WHAT IT HAS, not by a version field, because the
// files already written carry no version and never will. A flat file has a
// top-level `token`; a current one has `accounts`.
//
// The migrated profile is named from the token's own `username` claim, so the
// account keeps the name its user knows it by. A token that will not parse
// yields "default" — which is still a working session, just under a name nobody
// chose, and is preferable to discarding credentials that work.
func migrate(raw map[string]json.RawMessage) sessionFile {
	var out sessionFile
	if accounts, ok := raw["accounts"]; ok {
		_ = json.Unmarshal(accounts, &out.Accounts)
		if cur, ok := raw["current"]; ok {
			_ = json.Unmarshal(cur, &out.Current)
		}
		if out.Accounts == nil {
			out.Accounts = map[string]AccountProfile{}
		}
		return out
	}

	out.Accounts = map[string]AccountProfile{}
	var flat AccountProfile
	if t, ok := raw["token"]; ok {
		_ = json.Unmarshal(t, &flat.Token)
	}
	if rt, ok := raw["refresh_token"]; ok {
		_ = json.Unmarshal(rt, &flat.RefreshToken)
	}
	if s, ok := raw["active_slice"]; ok {
		_ = json.Unmarshal(s, &flat.ActiveSlice)
	}
	if flat.Token == "" && flat.RefreshToken == "" && flat.ActiveSlice == "" {
		return out
	}

	name := usernameFromToken(flat.Token)
	if name == "" {
		name = "default"
	}
	out.Accounts[name] = flat
	out.Current = name
	return out
}

func writeSession(s sessionFile) error {
	path, err := expandPath(SessionFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// CurrentAccount is the profile name this command acts as.
//
// The environment wins and writes nothing — see AccountEnv. An empty answer
// means there is no session at all, which is different from a session whose
// current account has no token.
func CurrentAccount() string {
	if a := strings.TrimSpace(os.Getenv(AccountEnv)); a != "" {
		return a
	}
	s, err := readSession()
	if err != nil {
		return ""
	}
	return s.Current
}

// Accounts lists every logged-in profile and which one is current.
func Accounts() (names []string, current string, err error) {
	s, rerr := readSession()
	if rerr != nil {
		return nil, "", rerr
	}
	for n := range s.Accounts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, CurrentAccount(), nil
}

// ActiveSliceFor reports one account's own active slice, for the listing.
//
// It does NOT consult DRIFT_SLICE, unlike GetActiveSlice: this answers "what is
// stored against this profile", and folding an environment override into it
// would make `drift account list` report the same slice for every account.
func ActiveSliceFor(name string) string {
	s, err := readSession()
	if err != nil {
		return ""
	}
	return s.Accounts[name].ActiveSlice
}

// UseAccount makes name the current profile.
func UseAccount(name string) error {
	s, err := readSession()
	if err != nil {
		return fmt.Errorf("no session — run 'drift account login' first")
	}
	if _, ok := s.Accounts[name]; !ok {
		return fmt.Errorf("no account named %q — run 'drift account list' to see which are logged in", name)
	}
	s.Current = name
	return writeSession(s)
}

// SaveSession persists a freshly-issued token pair.
//
// It writes into the account the token itself NAMES, not into whichever profile
// happened to be current. That is what makes `drift account login` ADD rather
// than replace: logging in as a second account cannot overwrite the first, and
// re-logging in as the same one lands in the same profile and keeps its active
// slice.
func SaveSession(token, refreshToken string) error {
	s, err := readSession()
	if err != nil {
		s = sessionFile{Accounts: map[string]AccountProfile{}}
	}
	if s.Accounts == nil {
		s.Accounts = map[string]AccountProfile{}
	}

	name := usernameFromToken(token)
	if name == "" {
		// A token whose claims will not parse still has to go somewhere, and the
		// current profile is the least surprising place — it is the account the
		// user was already acting as.
		name = s.Current
		if name == "" {
			name = "default"
		}
	}

	p := s.Accounts[name] // zero value when new; keeps ActiveSlice when not
	p.Token = token
	p.RefreshToken = refreshToken
	s.Accounts[name] = p
	s.Current = name
	return writeSession(s)
}

// GetTokenFromSession returns the current account's token pair.
func GetTokenFromSession() (token string, refreshToken string, err error) {
	s, err := readSession()
	if err != nil {
		return "", "", err
	}
	name := CurrentAccount()
	p, ok := s.Accounts[name]
	if !ok {
		if name != "" && os.Getenv(AccountEnv) != "" {
			return "", "", fmt.Errorf("no account named %q — run 'drift account list'", name)
		}
		return "", "", fmt.Errorf("no account is logged in")
	}
	if p.Token == "" || p.RefreshToken == "" {
		return "", "", fmt.Errorf("token or refresh_token not found in session file")
	}
	return p.Token, p.RefreshToken, nil
}

// ClearSession removes the CURRENT account from the session.
//
// Used after the account it belonged to is deleted, so the CLI stops holding
// credentials for an account that no longer exists. It removes that account
// only — deleting one account must not log the user out of the others — and
// picks a remaining one as current so the next command has somewhere to go. The
// file is removed entirely when nothing is left, which keeps "no session" as a
// missing file rather than an empty object.
func ClearSession() error {
	s, err := readSession()
	if err != nil {
		// No file, or an unreadable one. Either way there is nothing to clear,
		// and a missing file is not an error.
		return removeSessionFile()
	}
	delete(s.Accounts, CurrentAccount())
	if len(s.Accounts) == 0 {
		return removeSessionFile()
	}
	s.Current = ""
	for n := range s.Accounts {
		if s.Current == "" || n < s.Current {
			s.Current = n
		}
	}
	return writeSession(s)
}

func removeSessionFile() error {
	path, err := expandPath(SessionFile)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// readSessionMap loads the raw session JSON, untyped.
//
// RawMessage rather than string, because the file is no longer flat: `accounts`
// is an object, and a map[string]string decode fails on the whole file the
// moment one value is not a string.
func readSessionMap() (map[string]json.RawMessage, error) {
	path, err := expandPath(SessionFile)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make(map[string]json.RawMessage)
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// SaveActiveSlice persists the active slice against the CURRENT account.
func SaveActiveSlice(name string) error {
	s, err := readSession()
	if err != nil {
		return fmt.Errorf("no active session — log in first")
	}
	cur := CurrentAccount()
	p, ok := s.Accounts[cur]
	if !ok {
		return fmt.Errorf("no active session — log in first")
	}
	p.ActiveSlice = name
	s.Accounts[cur] = p
	return writeSession(s)
}

// GetActiveSlice returns the active slice name, or empty string if none set.
//
// DRIFT_SLICE WINS, and it has to. A personal access token carries an account
// rather than a slice, so a scripted caller has no `drift slice use` to have
// run and no profile to have recorded it in. Reading the environment first also
// makes the override behave like every other one in this CLI: an explicit value
// beats a stored one, so a pipeline on a developer's own machine targets the
// slice it names rather than whichever one that developer last used.
//
// Below that it is PER ACCOUNT, which is the bug the flat file had: one stored
// slice, shared by every account that logged in, pointing at a slice the current
// account may not own.
func GetActiveSlice() string {
	if s := strings.TrimSpace(os.Getenv(SliceEnv)); s != "" {
		return s
	}
	sess, err := readSession()
	if err != nil {
		return ""
	}
	return sess.Accounts[CurrentAccount()].ActiveSlice
}

// decodeTokenClaims decodes a JWT's payload segment (base64url, unverified —
// the CLI has no way to check the signature; it only reads its own claims
// for display/expiry purposes, the server is the real authority) into v.
func decodeTokenClaims(token string, v any) error {
	// JWT is three base64url-encoded segments separated by dots.
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("malformed token")
	}
	// base64url → base64 (add padding).
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.NewReplacer("-", "+", "_", "/").Replace(payload))
	if err != nil {
		return err
	}
	return json.Unmarshal(decoded, v)
}

// usernameFromToken reads the `username` claim, or "" if it cannot.
//
// This is what names a profile, so it is the one place the mapping from a
// credential to an account NAME lives — the migration, the login write and the
// whoami display all go through it rather than deriving a name three ways.
func usernameFromToken(token string) string {
	if token == "" {
		return ""
	}
	var claims struct {
		Username string `json:"username"`
	}
	if decodeTokenClaims(token, &claims) != nil {
		return ""
	}
	return claims.Username
}

// GetUsername extracts the username from the JWT access token this invocation is
// acting with.
//
// Returns an empty string if there is no session and no exchanged token, or if
// the token cannot be parsed. A PAT caller has no session file, so the name comes
// out of the token the exchange produced — which carries the same `username`
// claim a login's does, because it is the same kind of token.
func GetUsername() string {
	token, _, err := currentAccessToken()
	if err != nil || token == "" {
		return ""
	}
	return usernameFromToken(token)
}

// TokenExpired reports whether the stored access token's `exp` claim has
// already passed, checked locally against the system clock — no network
// round trip. Treated as expired (true) if there's no session, no token, or
// the token can't be parsed, since none of those give the caller anything
// usable either.
func TokenExpired() bool {
	// A PAT caller's access token is minted on demand and re-minted on a 401, so
	// "expired" is never a thing they need to act on — and answering true would
	// send a pipeline down a re-login path meant for a person.
	if PersonalAccessToken() != "" {
		return false
	}
	token, _, err := GetTokenFromSession()
	if err != nil || token == "" {
		return true
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if decodeTokenClaims(token, &claims) != nil || claims.Exp == 0 {
		return true
	}
	return time.Now().Unix() >= claims.Exp
}

// GetOrCreateDeviceID returns a stable per-workstation random ID. Used to
// bind refresh tokens: the server stores the ID at login, and a presented
// refresh token whose device_id doesn't match is treated as theft (revokes
// every live token for that user). Read-once cached on first call.
//
// Stored alongside the session file at ~/.drift/device_id with mode 0600.
// A stealer that copies session.json without device_id is locked out at
// the next refresh.
//
// PER MACHINE, NOT PER ACCOUNT, and that stays true with profiles: the server
// binds a refresh token to the workstation it was issued to, and two accounts on
// one laptop are one workstation. A per-account id would make the binding claim
// something it does not mean.
func GetOrCreateDeviceID() string {
	path, err := expandPath("~/.drift/device_id")
	if err != nil {
		return ""
	}
	if data, err := os.ReadFile(path); err == nil { // #nosec G304
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return id
	}
	_ = os.WriteFile(path, []byte(id), 0o600)
	return id
}

// RequireActiveSlice returns the active slice or an error instructing the user
// to select one with "drift slice use <name>".
func RequireActiveSlice() (string, error) {
	s := GetActiveSlice()
	if s == "" {
		return "", fmt.Errorf("no active slice — run 'drift slice use <name>' or 'drift slice create <name>' first")
	}
	return s, nil
}
