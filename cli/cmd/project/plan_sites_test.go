package project

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ondrift/cloud/cli/common"
)

// A site the plan compares is compared by the digest of its folder against the
// digest the slice recorded when the site was deployed. The slice stores the
// digest unread, so the plan and `drift apply` read one function for it
// (common.FolderDigest) and cannot disagree about what a match is.

// siteFolderDigest is the digest of the fxevm fixture's one site, which is what
// the slice holds for it when it was deployed from this folder.
func siteFolderDigest(t *testing.T) string {
	t.Helper()
	digest, err := common.FolderDigest(filepath.Join(mustAbs(t, "testdata"), "fxevm", "canvas"))
	if err != nil {
		t.Fatalf("digest of the fixture's site: %v", err)
	}
	return digest
}

// planWithSites runs `drift plan` on the slice that is the fxevm file in
// everything but its site registry, which answers body.
func planWithSites(t *testing.T, body string) string {
	t.Helper()
	live, deployed := matchingSlice(t)
	planStubWith(t, live, deployed, map[string]string{"/ops/canvas": body})
	out, err := planIn(t, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return out
}

// siteLineOf is the plan's line for the site mounted at "/".
func siteLineOf(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "site /") {
			return line
		}
	}
	t.Fatalf("the plan has no line for the site at /:\n%s", out)
	return ""
}

func siteRegistry(slug, route, digestField string) string {
	return fmt.Sprintf(`{"deployed":true,"sites":[{"slug":%q,"route":%q%s}]}`, slug, route, digestField)
}

// A site deployed from this very folder is unchanged, and a slice that is
// otherwise the file has nothing to change.
func TestPlan_ASiteWhoseDigestMatchesItsFolderIsUnchanged(t *testing.T) {
	digest := siteFolderDigest(t)

	out := planWithSites(t, siteRegistry("default", "/", fmt.Sprintf(`,"digest":%q`, digest)))

	line := siteLineOf(t, out)
	if want := "unchanged (digest " + shortDigest(digest) + ")"; !strings.Contains(line, want) {
		t.Errorf("the site line is %q, want it to say %q", line, want)
	}
	if strings.Contains(line, "~") || strings.Contains(line, "+") {
		t.Errorf("a site that matches its folder carries a mark: %q", line)
	}
	if !strings.Contains(out, closingMatch) {
		t.Errorf("a slice whose every fact matches does not say %q:\n%s", closingMatch, out)
	}
}

// A digest that is not the folder's is a difference the plan marks, and it shows
// both digests.
func TestPlan_ASiteWhoseDigestDiffersFromItsFolderIsChanged(t *testing.T) {
	digest := siteFolderDigest(t)

	out := planWithSites(t, siteRegistry("default", "/", `,"digest":"cd34cd34cd34"`))

	line := siteLineOf(t, out)
	if !strings.Contains(line, "~ site /") {
		t.Errorf("a site whose files changed is not marked ~: %q", line)
	}
	if want := "files changed (digest cd34… → " + shortDigest(digest) + ")"; !strings.Contains(line, want) {
		t.Errorf("the site line is %q, want it to say %q", line, want)
	}
	if strings.Contains(out, closingMatch) {
		t.Errorf("a plan with a changed site says %q:\n%s", closingMatch, out)
	}
}

// A slug the slice lacks is still an addition, and a route the slice holds
// differently from the file is a change that shows both routes.
func TestPlan_ASiteTheSliceLacksIsAddedAndOneAtAnotherRouteIsChanged(t *testing.T) {
	digest := fmt.Sprintf(`,"digest":%q`, siteFolderDigest(t))

	t.Run("a site the slice lacks", func(t *testing.T) {
		added := siteLineOf(t, planWithSites(t, `{"deployed":true,"sites":[]}`))
		if !strings.Contains(added, "+ site /") || !strings.Contains(added, "uploads canvas") {
			t.Errorf("a site the slice lacks reads %q, want it added with the folder it uploads", added)
		}
	})

	t.Run("a site at another route", func(t *testing.T) {
		moved := siteLineOf(t, planWithSites(t, siteRegistry("default", "/old", digest)))
		if !strings.Contains(moved, "~ site /") {
			t.Errorf("a site at another route is not marked ~: %q", moved)
		}
		if !strings.Contains(moved, "/old") || !strings.Contains(moved, "→ /") {
			t.Errorf("the site line is %q, want it to show the live route /old and the file's route /", moved)
		}
	})
}

// A site the slice holds no digest for (an older platform, or a site deployed
// before the platform kept one) is not compared, and a plan that did not compare
// it is not a match.
func TestPlan_ASiteWithNoDigestIsNotComparedAndIsUnread(t *testing.T) {
	for name, body := range map[string]string{
		"an empty digest": siteRegistry("default", "/", `,"digest":""`),
		"no digest field": siteRegistry("default", "/", ""),
	} {
		t.Run(name, func(t *testing.T) {
			out := planWithSites(t, body)

			line := siteLineOf(t, out)
			if want := "files not compared: the slice holds no digest for it"; !strings.Contains(line, want) {
				t.Errorf("the site line is %q, want it to say %q", line, want)
			}
			if strings.Contains(line, "~") || strings.Contains(line, "+") {
				t.Errorf("a site that was not compared carries a mark: %q", line)
			}
			if strings.Contains(out, closingMatch) {
				t.Errorf("a plan that did not compare a site says %q:\n%s", closingMatch, out)
			}
			_, unread, found := strings.Cut(out, closingUnread)
			if !found {
				t.Fatalf("the plan does not say %q:\n%s", closingUnread, out)
			}
			if !strings.Contains(unread, "the slice holds no digest for it") {
				t.Errorf("the plan does not list the site among what it could not read:\n%s", unread)
			}
			if got := headerOf(t, out); got != "unchanged (not fully read)" {
				t.Errorf("the header says %q, want %q", got, "unchanged (not fully read)")
			}
		})
	}
}
