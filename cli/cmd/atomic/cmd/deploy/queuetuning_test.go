package atomic_cmd

import "testing"

// A queue pulse ships the trigger its Driftfile declared: the tuning it wrote,
// and the slice's own defaults for what it left out.
func TestTriggersForCarriesAQueuePulsesTuning(t *testing.T) {
	for _, tc := range []struct {
		name         string
		poll, retry  int
		wantPoll     int
		wantMaxRetry int
	}{
		{"nothing declared", 0, 0, 500, 3},
		{"both declared", 250, 5, 250, 5},
		{"only the poll", 2000, 0, 2000, 3},
		{"only the retries", 0, 9, 500, 9},
	} {
		got := triggersFor(ElementFunc{Spec: FunctionSpec{Name: "queue:orders", PollMS: tc.poll, MaxRetry: tc.retry}})
		if len(got) != 1 || got[0].Type != "queue" || got[0].Source != "orders" {
			t.Fatalf("%s: triggers = %+v, want the one queue trigger", tc.name, got)
		}
		if got[0].PollMS != tc.wantPoll || got[0].MaxRetry != tc.wantMaxRetry {
			t.Errorf("%s: poll %d, retries %d; want %d and %d", tc.name, got[0].PollMS, got[0].MaxRetry, tc.wantPoll, tc.wantMaxRetry)
		}
	}
}

// Tuning a queue pulse changes what its trigger is, so it has to move the
// declaration digest: otherwise `drift apply` skips the pulse as unchanged and
// the new interval never reaches the slice. Left out, the digest is what it was.
func TestDeclarationDigest_MovesWhenAQueuePulseIsTuned(t *testing.T) {
	base := FunctionSpec{Name: "queue:orders", Handler: "Handle"}
	tuned := base
	tuned.PollMS = 250
	retried := base
	retried.MaxRetry = 5

	seen := map[string]string{DeclarationDigest(base): "base"}
	for name, spec := range map[string]FunctionSpec{"poll": tuned, "retries": retried} {
		d := DeclarationDigest(spec)
		if prev, dup := seen[d]; dup {
			t.Errorf("tuning the %s left the digest where %s is", name, prev)
		}
		seen[d] = name
	}
}
