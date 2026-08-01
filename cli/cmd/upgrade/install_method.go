package upgrade

import (
	"os"
	"path/filepath"
	"strings"
)

// InstallMethod is how the running binary got onto this machine. `drift upgrade`
// has to know, because the two paths are mutually exclusive: running
// `go install` over a Homebrew-managed binary does not upgrade it. It writes a
// SECOND drift into GOPATH/bin, leaves Homebrew's records claiming the old
// version, and which one the user gets from then on depends on PATH order.
type InstallMethod int

const (
	// MethodUnknown covers a manually downloaded binary, a distro package, a
	// container image — anything we cannot positively identify. The command
	// refuses to guess in this case rather than picking one and being wrong.
	MethodUnknown InstallMethod = iota
	// MethodHomebrew is a binary under a Homebrew prefix.
	MethodHomebrew
	// MethodGoInstall is a binary under GOPATH/bin or GOBIN.
	MethodGoInstall
)

func (m InstallMethod) String() string {
	switch m {
	case MethodHomebrew:
		return "homebrew"
	case MethodGoInstall:
		return "go install"
	default:
		return "unknown"
	}
}

// brewPrefixes are the standard Homebrew roots: Apple Silicon, Intel macOS, and
// Linuxbrew. A custom HOMEBREW_PREFIX is handled separately via the environment.
var brewPrefixes = []string{
	"/opt/homebrew",
	"/usr/local/Cellar",
	"/usr/local/Homebrew",
	"/home/linuxbrew/.linuxbrew",
}

// DetectInstallMethod classifies the RUNNING binary by its own path.
//
// The path is resolved through symlinks first, and that is the whole trick:
// Homebrew puts the real binary in `<prefix>/Cellar/drift/<version>/bin/drift`
// and links it from `<prefix>/bin/drift`. Only the resolved path is reliably
// under a recognisable prefix — matching on the symlink alone would miss a
// custom prefix, and matching on os.Args[0] would miss everything, since that
// is whatever the user typed.
func DetectInstallMethod() (InstallMethod, string) {
	exe, err := os.Executable()
	if err != nil {
		return MethodUnknown, ""
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return classify(exe), exe
}

// classify decides the install method for an already-resolved path.
//
// Split out from DetectInstallMethod so the actual decision can be tested
// against real-world paths — "/opt/homebrew/bin/drift" and the like — without
// needing the test binary to physically live there. Testing only through
// DetectInstallMethod meant the prefix scan could be deleted entirely and every
// test still passed, which is exactly the regression this split closes.
func classify(exe string) InstallMethod {
	// An explicit HOMEBREW_PREFIX wins: someone who has moved their prefix has
	// told us where it is, and we should not fall back to guessing.
	if p := strings.TrimSpace(os.Getenv("HOMEBREW_PREFIX")); p != "" {
		if under(exe, p) {
			return MethodHomebrew
		}
	}
	for _, p := range brewPrefixes {
		if under(exe, p) {
			return MethodHomebrew
		}
	}

	// GOBIN wins over GOPATH when set, mirroring the go tool's own precedence.
	if b := strings.TrimSpace(os.Getenv("GOBIN")); b != "" {
		if under(exe, b) {
			return MethodGoInstall
		}
	}
	for _, gp := range gopaths() {
		if under(exe, filepath.Join(gp, "bin")) {
			return MethodGoInstall
		}
	}
	return MethodUnknown
}

// gopaths returns every entry on GOPATH, falling back to the default ~/go.
func gopaths() []string {
	if gp := strings.TrimSpace(os.Getenv("GOPATH")); gp != "" {
		return filepath.SplitList(gp)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, "go")}
}

// under reports whether path sits inside dir.
//
// Compares on a cleaned, separator-terminated prefix so that "/opt/homebrew-x"
// is not read as being inside "/opt/homebrew" — a plain strings.HasPrefix would
// say it is, and would then tell that user to run `brew upgrade` for a binary
// Homebrew has never heard of.
func under(path, dir string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}
