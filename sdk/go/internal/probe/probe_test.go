package probe

import "testing"

// The hello is the platform's literal for version 1, byte for byte: the SDKs
// repeat it, and nothing links the two sides but a test against each copy.
func TestHelloIsThePlatformsLine(t *testing.T) {
	const want = `{"hello":{"proto":"drift-probe","v":1}}` + "\n"
	if got := Hello(1); got != want {
		t.Fatalf("Hello(1) = %q, want %q", got, want)
	}
}

// Only DRIFT_INVOKE=probe<version> is a request for the check at that version:
// another version, the multiplexed protocol and nothing at all are not.
func TestRequestedMatchesTheWholeValue(t *testing.T) {
	for value, want := range map[string]bool{
		"probe1":  true,
		"probe2":  false,
		"probe":   false,
		"probe11": false,
		"mux1":    false,
		"":        false,
	} {
		t.Setenv("DRIFT_INVOKE", value)
		if got := Requested(1); got != want {
			t.Errorf("DRIFT_INVOKE=%q: Requested(1) = %v, want %v", value, got, want)
		}
	}
	t.Setenv("DRIFT_INVOKE", "probe7")
	if !Requested(7) || Requested(1) {
		t.Errorf("DRIFT_INVOKE=probe7 must request version 7 and no other")
	}
}
