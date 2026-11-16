package project

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// `drift apply` uploads a site only when the slice does not already hold it as
// the folder is: the slug, the route and the digest of the files all match. What
// it uploads carries the digest, so the next apply, and `drift plan`, can tell.

// siteProject is the shipsDriftfile project with one site folder holding index,
// served by a fake slice. It returns the slice, the site's folder and an apply of
// the canvas section whose output it returns.
func siteProject(t *testing.T) (slice *fakeSlice, folder string, apply func() string) {
	t.Helper()
	withRealSchema(t)
	slice = &fakeSlice{digests: map[string]string{}}
	stubAPI(t, slice.handler(t))

	path := writeDriftfile(t, shipsDriftfile)
	folder = filepath.Join(filepath.Dir(path), "site")
	writeFile(t, filepath.Join(filepath.Dir(path), "atomic", "hello.py"), "def get_hello(req):\n    return 200, 'OK', {}\n")
	writeFile(t, filepath.Join(folder, "index.html"), "<!doctype html>one")

	apply = func() string {
		t.Helper()
		m, err := ParseDriftfile(path)
		if err != nil {
			t.Fatalf("parsing the Driftfile: %v", err)
		}
		var out bytes.Buffer
		if err := applyCanvas(m, &out); err != nil {
			t.Fatalf("applyCanvas: %v", err)
		}
		return out.String()
	}
	return slice, folder, apply
}

func TestApply_ASecondApplyUploadsNoSiteThatMatches(t *testing.T) {
	slice, _, apply := siteProject(t)

	first := apply()
	if len(slice.sites) != 1 {
		t.Fatalf("the first apply uploaded %d sites, want the one it declares", len(slice.sites))
	}
	if !strings.Contains(first, "site → /") {
		t.Errorf("the first apply does not say which folder went to which route:\n%s", first)
	}

	second := apply()
	if len(slice.sites) != 1 {
		t.Errorf("an apply with nothing changed uploaded again (%d uploads in all)", len(slice.sites))
	}
	if !strings.Contains(second, "site unchanged") {
		t.Errorf("the apply does not say the site is unchanged:\n%s", second)
	}
}

func TestApply_AnUploadCarriesTheDigestOfTheFolder(t *testing.T) {
	slice, folder, apply := siteProject(t)

	apply()

	want, err := common.FolderDigest(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(slice.siteQueries) != 1 {
		t.Fatalf("%d site uploads, want 1", len(slice.siteQueries))
	}
	if got := slice.siteQueries[0].Get("digest"); got != want {
		t.Errorf("the upload carries digest %q, want the folder's %q", got, want)
	}
}

// A site that differs from the slice's in files, in route, or in having no
// recorded digest is uploaded again.
func TestApply_ASiteThatDiffersFromTheSlicesIsUploaded(t *testing.T) {
	cases := map[string]func(slice *fakeSlice, folder string){
		"its files changed": func(_ *fakeSlice, folder string) {
			writeFile(t, filepath.Join(folder, "index.html"), "<!doctype html>two")
		},
		"a file was added": func(_ *fakeSlice, folder string) {
			writeFile(t, filepath.Join(folder, "about.html"), "about")
		},
		"the slice holds it at another route": func(slice *fakeSlice, _ string) {
			slice.registry["default"] = siteSeen{route: "/old", digest: slice.registry["default"].digest}
		},
		"the slice holds no digest for it": func(slice *fakeSlice, _ string) {
			slice.registry["default"] = siteSeen{route: "/"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			slice, folder, apply := siteProject(t)
			apply()
			change(slice, folder)

			out := apply()

			if len(slice.sites) != 2 {
				t.Fatalf("%d uploads in all, want the second apply to upload the site", len(slice.sites))
			}
			if strings.Contains(out, "unchanged") {
				t.Errorf("an uploaded site is called unchanged:\n%s", out)
			}
			if !strings.Contains(out, "site → /") {
				t.Errorf("the apply does not say which folder went to which route:\n%s", out)
			}
			want, _ := common.FolderDigest(folder)
			if got := slice.siteQueries[1].Get("digest"); got != want {
				t.Errorf("the second upload carries digest %q, want the folder's %q", got, want)
			}
		})
	}
}

func TestApply_ForceUploadsEverySite(t *testing.T) {
	slice, _, apply := siteProject(t)
	apply()
	atomicForce = true
	t.Cleanup(func() { atomicForce = false })

	apply()

	if len(slice.sites) != 2 {
		t.Errorf("%d uploads in all, want --force to upload the site that matched", len(slice.sites))
	}
}

// A list that could not be read says nothing about what the slice holds, so
// every site goes up, as it did before the slice kept digests.
func TestApply_AFailedReadOfTheSiteListUploadsEverySite(t *testing.T) {
	slice, _, apply := siteProject(t)
	apply()
	slice.listFails = true

	apply()

	if len(slice.sites) != 2 {
		t.Errorf("%d uploads in all, want the site uploaded when the list could not be read", len(slice.sites))
	}
}
