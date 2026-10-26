package atomic_cmd

import "testing"

// The Go build container's environment, and the one entry that decides whether a
// user's `go` directive is a request or a wall.
//
// The official golang images set GOTOOLCHAIN=local, which makes the image tag a
// CEILING: a module declaring a newer patch than the image ships dies with
// `go.mod requires go >= X (running Y; GOTOOLCHAIN=local)` and the only escape a
// user has is DRIFT_BUILD_IMAGE_GO. Every module in the platform repo declares
// 1.26.6 and this image is 1.26.2, so that was every platform element.

// GOTOOLCHAIN=auto turns the tag into a FLOOR — Go fetches what the module asks
// for. Without it the pin has to be chased forward on every Go release, and a
// user on a newer patch than the CLI's image cannot deploy at all.
func TestGoBuildEnv_LetsTheModuleChooseItsToolchain(t *testing.T) {
	env := toolchainCacheEnv("go")
	if got := env["GOTOOLCHAIN"]; got != "auto" {
		t.Errorf("GOTOOLCHAIN = %q, want \"auto\" — the image tag becomes a ceiling without it", got)
	}
}

// The fetched toolchain lands in GOMODCACHE, which is the mounted /cache, so it
// is downloaded once per version per architecture rather than on every build.
// Pointing it at the container's own filesystem would make `auto` a per-build
// download instead of a one-off.
func TestGoBuildEnv_CachesOutsideTheContainer(t *testing.T) {
	env := toolchainCacheEnv("go")
	for _, k := range []string{"GOMODCACHE", "GOCACHE", "GOPATH"} {
		v := env[k]
		if v == "" {
			t.Errorf("%s is unset, so the build cannot reuse anything between runs", k)
			continue
		}
		if len(v) < 7 || v[:7] != "/cache/" {
			t.Errorf("%s = %q, want a path under the mounted /cache", k, v)
		}
	}
}

// The other languages are untouched — this is a Go-specific escape hatch, and a
// GOTOOLCHAIN leaking into a python or node container would be meaningless at
// best.
func TestGoBuildEnv_ToolchainIsGoOnly(t *testing.T) {
	for _, lang := range []string{"python", "node", "ruby", "php", "rust"} {
		if _, ok := toolchainCacheEnv(lang)["GOTOOLCHAIN"]; ok {
			t.Errorf("%s carries GOTOOLCHAIN", lang)
		}
	}
}
