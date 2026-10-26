package project

import (
	"strings"
	"testing"
)

// compileQueue compiles a slice with one function labelled as given and the extra
// lines inside its block.
func compileQueue(t *testing.T, label, extra string) (*Compiled, error) {
	t.Helper()
	withRealSchema(t)
	body := `slice "demo" {}

atomic {
  backend "default" {
    function "` + label + `" {
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
// queue`. They are a queue function's own attributes now, written to the document
// as poll_ms and max_retry, and reach the deploy as the function's trigger.
func TestAQueueFunctionDeclaresItsPollAndRetries(t *testing.T) {
	c, err := compileQueue(t, "QUEUE orders", "      poll = \"250ms\"\n      max_retries = 5")
	if err != nil {
		t.Fatalf("a queue function with poll and max_retries was refused: %v", err)
	}
	specs := FunctionSpecs(c.Manifest)
	if len(specs) != 1 || specs[0].PollMS != 250 || specs[0].MaxRetry != 5 {
		t.Errorf("the function's spec = %+v, want poll 250 ms and 5 retries", specs)
	}
}

// Left out, nothing is written, so the slice's own defaults apply and every
// document written before these existed is unchanged.
func TestAQueueFunctionThatSaysNothingWritesNothing(t *testing.T) {
	c, err := compileQueue(t, "QUEUE orders", "")
	if err != nil {
		t.Fatal(err)
	}
	if s := FunctionSpecs(c.Manifest); s[0].PollMS != 0 || s[0].MaxRetry != 0 {
		t.Errorf("a function that declares no tuning carries %+v", s[0])
	}
}

// An HTTP function has no queue to look in, so the attributes are refused at the
// attribute rather than accepted and ignored.
func TestPollAndMaxRetriesBelongToAQueueFunction(t *testing.T) {
	for _, attr := range []string{`poll = "250ms"`, `max_retries = 5`} {
		_, err := compileQueue(t, "POST /orders", "      "+attr)
		if err == nil || !strings.Contains(err.Error(), "Not a queue function") {
			t.Errorf("%s on an HTTP function: %v; want it refused as not a queue function", attr, err)
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
