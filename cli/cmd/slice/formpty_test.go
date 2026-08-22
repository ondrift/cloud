package slice

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// formpty_test.go — driving the form through a real terminal.
//
// Everything else about the form is tested by calling into it: handle() takes a
// key, collect() returns a shape. That leaves the part that actually runs when
// somebody types — raw mode, the key decoder, the select loop, and the submit
// that answers a refusal without leaving the screen — proven by nothing.
//
// A pty is what makes that testable. term.MakeRaw needs a terminal, so the test
// makes one: /dev/ptmx opened directly, which costs no dependency because
// golang.org/x/sys is already in the build.
//
// The master side MUST be drained for as long as the form is up. The form
// redraws on every keystroke and every price tick, and a pty whose buffer fills
// blocks the writer — so a test that only reads at the end deadlocks the thing
// it is testing.

// openPTY returns the two ends of a fresh pseudo-terminal.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx here: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("cannot unlock the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("cannot name the pty: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("cannot open the pty slave: %v", err)
	}
	t.Cleanup(func() { s.Close(); m.Close() })
	return m, s
}

// screen collects everything the form draws, so a test can assert on what the
// person in front of it would read.
type screen struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// runFormOnPTY puts the form on a real terminal and returns the screen it drew
// and a function that sends keystrokes to it.
func runFormOnPTY(t *testing.T, f *shapeForm) (*screen, func(string), <-chan error) {
	t.Helper()
	master, slave := openPTY(t)

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = slave, slave
	t.Cleanup(func() { os.Stdin, os.Stdout = oldIn, oldOut })

	drawn := &screen{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := master.Read(b)
			if n > 0 {
				drawn.mu.Lock()
				drawn.buf.Write(b[:n])
				drawn.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, _, _, err := runForm(f)
		done <- err
	}()

	// Wait for the first frame before letting anything be typed.
	//
	// Until runForm has called MakeRaw the terminal is still canonical, where
	// Ctrl-C is a signal rather than a byte and nothing is delivered before a
	// newline — so a keystroke sent into that window is not the keystroke the
	// form receives, and the test would be racing the thing it is testing.
	deadline := time.Now().Add(3 * time.Second)
	for drawn.String() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the form drew nothing; it never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	send := func(keys string) {
		// A beat between keystrokes: the loop reads one key, redraws, and comes
		// back for the next. Writing the whole sequence at once still works, but
		// pacing it is what a person does and is what the decoder's Esc peek is
		// written against.
		for _, r := range keys {
			_, _ = master.Write([]byte(string(r)))
			time.Sleep(15 * time.Millisecond)
		}
	}
	return drawn, send, done
}

// downTo is enough Down presses to land on the last row, whatever is above it.
// The cursor clamps at the end, so overshooting is safe and does not depend on
// how many rows a given shape happens to draw.
const downTo = "\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B" +
	"\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B"

// The whole refusal round trip, on a terminal: submit, be told the price moved,
// press again, and have the second attempt carry the acknowledgement.
//
// This is the flow the browser has and the terminal did not, and the one the
// slices site cannot be retired without.
func TestFormAnswersAPriceRefusalWithoutLeavingTheScreen(t *testing.T) {
	f := newShapeForm("lab")
	fillFromConfig(f, configFrom(t, `{
		"atomic": {"MaxNumberOfFunctions": 1,
			"functions": [{"route": "probe", "method": "get", "memory_bytes": 33554432}]},
		"backbone": {}, "canvas": {}
	}`))
	f.createNode.label = "Apply"

	var attempts int
	var acknowledged bool
	f.onSubmit = func(shape declaredShape, name string) (bool, error) {
		attempts++
		if attempts == 1 {
			// What the operator answers a first, unacknowledged attempt with.
			f.status = "This resize moves what you pay, from EUR 4.05 to EUR 1.28 a month. " +
				"Press Apply again to agree to it."
			f.createNode.label = "Agree to EUR 1.28/mo and apply"
			return false, nil
		}
		acknowledged = true
		return true, nil
	}

	drawn, send, done := runFormOnPTY(t, f)

	send(downTo + "\r") // land on Apply, press it
	time.Sleep(150 * time.Millisecond)

	if attempts != 1 {
		t.Fatalf("the first press submitted %d times, want 1", attempts)
	}
	if acknowledged {
		t.Fatal("the first press was treated as an agreement to the new price")
	}

	send("\r") // press again — this IS the agreement

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the form returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the form did not finish after the second press")
	}

	if attempts != 2 {
		t.Errorf("submitted %d times, want 2 — the second press is the agreement", attempts)
	}
	if !acknowledged {
		t.Error("the second press did not carry the acknowledgement")
	}

	// The person in front of it was told both figures and what pressing again
	// would mean, on the screen, without the form coming down.
	out := drawn.String()
	if !strings.Contains(out, "Press Apply again to agree") {
		t.Error("the refusal never reached the screen")
	}
	if !strings.Contains(out, "Agree to EUR 1.28/mo and apply") {
		t.Error("the action row never said what pressing it would now do")
	}
}

// Ctrl-C leaves without submitting, and says so by submitting nothing.
//
// The control: it proves the harness drives a real form rather than one that
// finishes whatever it is sent.
func TestFormOnATerminalLeavesWithoutSubmitting(t *testing.T) {
	f := newShapeForm("lab")
	fillFromConfig(f, configFrom(t, `{"atomic": {}, "backbone": {}, "canvas": {}}`))

	var attempts int
	f.onSubmit = func(declaredShape, string) (bool, error) {
		attempts++
		return true, nil
	}

	_, send, done := runFormOnPTY(t, f)
	send("\x03") // Ctrl-C

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("quitting returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not end the form")
	}
	if attempts != 0 {
		t.Errorf("quitting submitted %d times, want 0", attempts)
	}
}
