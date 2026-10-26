package project

import (
	"strings"
	"testing"
)

// compileQueue compiles a slice with one pulse labelled as given and the extra
// lines inside its block.
func compileQueue(t *testing.T, label, extra string) (*Compiled, error) {
	t.Helper()
	withRealSchema(t)
	body := `slice "demo" {}

atomic {
  element "default" {
    pulse "` + label + `" {
      handler = "Handle"
      memory = "16MiB"
` + extra + `
    }
  }
}

backbone {
  queue "orders" {}
}
`
	return CompileDriftfile(writeDriftfile(t, body, "atomic/"))
}

// `poll` and `max_retries` were the two knobs of `drift atomic trigger register
// queue`. They are a queue pulse's own attributes now, written to the document
// as poll_ms and max_retry, and reach the deploy as the pulse's trigger.
func TestAQueuePulseDeclaresItsPollAndRetries(t *testing.T) {
	c, err := compileQueue(t, "QUEUE orders", "      poll = \"250ms\"\n      max_retries = 5")
	if err != nil {
		t.Fatalf("a queue pulse with poll and max_retries was refused: %v", err)
	}
	specs := FunctionSpecs(c.Manifest)
	if len(specs) != 1 || specs[0].PollMS != 250 || specs[0].MaxRetry != 5 {
		t.Errorf("the pulse's spec = %+v, want poll 250 ms and 5 retries", specs)
	}
}

// Left out, nothing is written, so the slice's own defaults apply and every
// document written before these existed is unchanged.
func TestAQueuePulseThatSaysNothingWritesNothing(t *testing.T) {
	c, err := compileQueue(t, "QUEUE orders", "")
	if err != nil {
		t.Fatal(err)
	}
	if s := FunctionSpecs(c.Manifest); s[0].PollMS != 0 || s[0].MaxRetry != 0 {
		t.Errorf("a pulse that declares no tuning carries %+v", s[0])
	}
}

// An HTTP pulse has no queue to look in, so the attributes are refused at the
// attribute rather than accepted and ignored.
func TestPollAndMaxRetriesBelongToAQueuePulse(t *testing.T) {
	for _, attr := range []string{`poll = "250ms"`, `max_retries = 5`} {
		_, err := compileQueue(t, "POST /orders", "      "+attr)
		if err == nil || !strings.Contains(err.Error(), "Not a queue pulse") {
			t.Errorf("%s on an HTTP pulse: %v; want it refused as not a queue pulse", attr, err)
		}
	}
}

func TestPollAndMaxRetriesAreBounded(t *testing.T) {
	for attr, want := range map[string]string{
		`poll = "10ms"`:       "Out of range",
		`poll = "2m"`:         "Out of range",
		`poll = "soon"`:       "Not a duration",
		`max_retries = 0`:     "Out of range",
		`max_retries = 101`:   "Out of range",
		`max_retries = 2.5`:   "whole number",
		`max_retries = "two"`: "",
	} {
		_, err := compileQueue(t, "QUEUE orders", "      "+attr)
		if err == nil {
			t.Errorf("%s was accepted", attr)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v; want it to say %q", attr, err, want)
		}
	}
	for _, ok := range []string{`poll = "50ms"`, `poll = "60s"`, `max_retries = 1`, `max_retries = 100`} {
		if _, err := compileQueue(t, "QUEUE orders", "      "+ok); err != nil {
			t.Errorf("%s was refused: %v", ok, err)
		}
	}
}
