package project

import (
	"strings"
	"testing"
)

func TestUnifiedDiff_IdenticalIsEmpty(t *testing.T) {
	if got := UnifiedDiff("a", "b", []byte("x\ny\n"), []byte("x\ny\n")); got != "" {
		t.Errorf("identical inputs produced a diff:\n%s", got)
	}
}

// Changes far apart are separate hunks, each with three lines of context and
// the line numbers of its own side.
func TestUnifiedDiff_DistantChangesAreSeparateHunks(t *testing.T) {
	var a, b []string
	for i := 1; i <= 20; i++ {
		a = append(a, "line"+string(rune('a'+i)))
	}
	b = append(b, a...)
	b[1] = "changed-early"
	b = append(b[:17], append([]string{"inserted-late"}, b[17:]...)...)

	got := UnifiedDiff("a/f", "b/f", []byte(strings.Join(a, "\n")+"\n"), []byte(strings.Join(b, "\n")+"\n"))
	want := "--- a/f\n+++ b/f\n" +
		"@@ -1,5 +1,5 @@\n linebx\n-linec\n+changed-early\n linedx\n linee\n linef\n"
	want = strings.ReplaceAll(want, "linebx", "lineb")
	want = strings.ReplaceAll(want, "linedx", "lined")
	want += "@@ -15,6 +15,7 @@\n linep\n lineq\n liner\n+inserted-late\n lines\n linet\n lineu\n"
	if got != want {
		t.Errorf("hunks:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// A last line without a newline is marked, as `diff -u` marks it, so the patch
// reproduces the file exactly.
func TestUnifiedDiff_MarksAMissingFinalNewline(t *testing.T) {
	got := UnifiedDiff("a/f", "b/f", []byte("x\ny"), []byte("x\nz"))
	want := "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n x\n-y\n\\ No newline at end of file\n+z\n\\ No newline at end of file\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
