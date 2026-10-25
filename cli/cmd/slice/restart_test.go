package slice

import (
	"strings"
	"testing"
)

// A restart replaces the slice's only instance: calls in flight drain, new ones
// get a 503, and it comes back after tens of seconds. The note promised "a few
// seconds", which a person plans a deploy around.
func TestTheRestartNoticeDoesNotPromiseAFewSeconds(t *testing.T) {
	got := restartNotice("my-shop")
	if strings.Contains(got, "a few seconds") {
		t.Errorf("the restart notice still promises a few seconds: %q", got)
	}
	for _, want := range []string{"my-shop", "tens of seconds", "503"} {
		if !strings.Contains(got, want) {
			t.Errorf("the restart notice does not say %q: %q", want, got)
		}
	}
	for _, leak := range []string{"pod", "Pod", "Kubernetes", "kubernetes", "node"} {
		if strings.Contains(got, leak) {
			t.Errorf("the restart notice names infrastructure (%q): %q", leak, got)
		}
	}
}
