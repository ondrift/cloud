package project

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freeSlotsAre answers the account's free slices without a platform.
func freeSlotsAre(t *testing.T, limit int, held ...string) {
	t.Helper()
	old := fetchFreeSlots
	fetchFreeSlots = func() (FreeSlots, error) {
		f := FreeSlots{Limit: limit}
		for i, name := range held {
			f.Held = append(f.Held, FreeSliceRef{Name: name, Slot: i + 1})
		}
		return f, nil
	}
	t.Cleanup(func() { fetchFreeSlots = old })
}

// freeApplyFixture is a Driftfile that fits the free grant, for a slice that
// does not exist yet.
func freeApplyFixture(t *testing.T, file string) (*applyPlatform, *int) {
	t.Helper()
	p := &applyPlatform{bodies: map[string][]map[string]any{}}
	stubAPI(t, p.handler(t))
	withRealSchema(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Driftfile"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("DRIFT_SLICE", "")

	deployed := 0
	old := deployInto
	deployInto = func(m *Manifest, out io.Writer) error { deployed++; return nil }
	t.Cleanup(func() { deployInto = old })
	return p, &deployed
}

func runFreeApply(t *testing.T, a applyAnswers, ask bool, input string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = runApply(applyOptions{noEnvFile: true, answers: a, ask: ask, out: os.Stdout, in: strings.NewReader(input)})
	})
	return out, err
}

func TestApply_ANewSliceInsideTheFreeGrantNeedsTheFreeSliceAnswered(t *testing.T) {
	freeSlotsAre(t, 3, "one", "two")
	p, _ := freeApplyFixture(t, "slice \"demo\" {}\n")

	_, err := runFreeApply(t, noAnswers(), false, "")
	if err == nil || !strings.Contains(err.Error(), "--use-free-slice") {
		t.Fatalf("an unanswered free slice was not refused with its flag: %v", err)
	}
	if len(p.writes) != 0 {
		t.Fatalf("something was sent before the free slice was agreed: %v", p.writes)
	}

	a := noAnswers()
	a.useFree = true
	if _, err := runFreeApply(t, a, false, ""); err != nil {
		t.Fatalf("apply --use-free-slice: %v", err)
	}
	bodies := p.bodies["POST /ops/slice/create"]
	if len(bodies) != 1 || bodies[0]["tier"] != "hacker" {
		t.Fatalf("the free slice was not created as one: %v", bodies)
	}
}

func TestApply_TheFreeQuestionNamesTheNumbersAndNoMeansPriced(t *testing.T) {
	freeSlotsAre(t, 3, "one", "two")
	p, _ := freeApplyFixture(t, "slice \"demo\" {}\n")

	out, err := runFreeApply(t, noAnswers(), true, "y\n")
	if err != nil {
		t.Fatalf("apply answering yes: %v", err)
	}
	if !strings.Contains(out, "You have 2 of 3 free slices — make this the 3rd?") {
		t.Errorf("the question does not carry the platform's numbers:\n%s", out)
	}
	if got := p.bodies["POST /ops/slice/create"]; len(got) != 1 || got[0]["tier"] != "hacker" {
		t.Fatalf("yes did not create a free slice: %v", got)
	}
}

func TestApply_AFullAccountIsRefusedNamingItsFreeSlices(t *testing.T) {
	freeSlotsAre(t, 3, "alpha", "beta", "gamma")
	p, _ := freeApplyFixture(t, "slice \"demo\" {}\n")

	a := noAnswers()
	a.useFree = true
	_, err := runFreeApply(t, a, false, "")
	if err == nil {
		t.Fatal("a fourth free slice was applied")
	}
	for _, want := range []string{"alpha", "beta", "gamma", "Empty one first", "--approve-price", "does not free its slot"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
	if len(p.writes) != 0 {
		t.Fatalf("something was sent to a full account: %v", p.writes)
	}
}

func TestApply_ChoosingPricedOverAnOpenFreeSlotNeedsTheQuotedPrice(t *testing.T) {
	freeSlotsAre(t, 3, "one")
	p, _ := freeApplyFixture(t, "slice \"demo\" {}\n")

	a := noAnswers()
	a.price = 0
	if _, err := runFreeApply(t, a, false, ""); err != nil {
		t.Fatalf("apply --approve-price 0: %v", err)
	}
	bodies := p.bodies["POST /ops/slice/create"]
	if len(bodies) != 1 || bodies[0]["tier"] != "custom" {
		t.Fatalf("agreeing to a price did not create a priced slice: %v", bodies)
	}
}

func TestApply_APlatformWithoutTheFreeRouteStillAsksWithoutNumbers(t *testing.T) {
	old := fetchFreeSlots
	fetchFreeSlots = func() (FreeSlots, error) { return FreeSlots{}, errors.New("not found") }
	t.Cleanup(func() { fetchFreeSlots = old })
	_, _ = freeApplyFixture(t, "slice \"demo\" {}\n")

	out, err := runFreeApply(t, noAnswers(), true, "y\n")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out, "Make this one of your free slices?") {
		t.Errorf("the generic question was not asked:\n%s", out)
	}
}

func TestApply_ABillingKeyIsRefusedBeforeAnythingIsSent(t *testing.T) {
	freeSlotsAre(t, 3)
	p, _ := freeApplyFixture(t, "slice \"demo\" {\n  billing = \"monthly\"\n}\n")

	_, err := runFreeApply(t, noAnswers(), false, "")
	if err == nil || !strings.Contains(err.Error(), "billing") {
		t.Fatalf("billing = \"monthly\" was not refused: %v", err)
	}
	if len(p.writes) != 0 {
		t.Fatalf("something was sent: %v", p.writes)
	}
}

func TestFreeSlots_QuestionAndOrdinals(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 21: "21st"} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
	f := FreeSlots{Limit: 4, Held: []FreeSliceRef{{Name: "a"}, {Name: "b"}}}
	if f.Open() != 2 || f.Full() {
		t.Errorf("4 allowed, 2 held: open %d, full %v", f.Open(), f.Full())
	}
	if got := f.freeQuestion(); got != "You have 2 of 4 free slices — make this the 3rd?" {
		t.Errorf("question = %q", got)
	}
}
