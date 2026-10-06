package drift

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// runWSWith runs RunWS with stdin carrying input and returns what the handler
// read and what it wrote. RunWS reads the process's own stdin and stdout, so
// both are swapped for pipes for the length of the call.
func runWSWith(t *testing.T, input string, handler func(Request, Conn)) string {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()

	go func() {
		_, _ = io.WriteString(inW, input)
		_ = inW.Close()
	}()
	written := make(chan string)
	go func() {
		b, _ := io.ReadAll(outR)
		written <- string(b)
	}()

	RunWS(handler)
	_ = outW.Close()
	_ = inR.Close()
	return <-written
}

// A WebSocket message longer than 64 KiB reaches the handler whole, and the
// connection carries on after it. A line scanner's token limit ended the
// connection at the first such message.
func TestRunWSReadsAMessageOver64KiB(t *testing.T) {
	big := `{"blob":"` + strings.Repeat("x", 100*1024) + `"}`
	input := `{"path":"/chat"}` + "\n" + big + "\n" + `{"n":2}` + "\n"

	var got []json.RawMessage
	var path string
	runWSWith(t, input, func(req Request, conn Conn) {
		path = req.Path
		for {
			msg, ok := conn.Read()
			if !ok {
				return
			}
			got = append(got, append(json.RawMessage(nil), msg...))
		}
	})

	if path != "/chat" {
		t.Fatalf("request path = %q, want /chat", path)
	}
	if len(got) != 2 {
		t.Fatalf("handler read %d messages, want 2", len(got))
	}
	if !bytes.Equal(got[0], []byte(big)) {
		t.Fatalf("first message is %d bytes, want the %d-byte message whole", len(got[0]), len(big))
	}
	if string(got[1]) != `{"n":2}` {
		t.Fatalf("second message = %q", got[1])
	}
}

// A first line over 64 KiB, the initial request with a large body, is read
// whole as well.
func TestRunWSReadsARequestOver64KiB(t *testing.T) {
	body := strings.Repeat("y", 100*1024)
	input := `{"path":"/up","body":"` + body + `"}` + "\n"

	var got Request
	called := false
	runWSWith(t, input, func(req Request, _ Conn) {
		called = true
		got = req
	})
	if !called {
		t.Fatal("the handler never ran")
	}
	var s string
	if err := json.Unmarshal(got.Body, &s); err != nil || s != body {
		t.Fatalf("request body is %d bytes (err %v), want %d", len(s), err, len(body))
	}
}

// Line endings read as a line scanner read them: a trailing CR is dropped, an
// empty line is an empty message, and a last line without a newline still
// arrives.
func TestRunWSLineEndings(t *testing.T) {
	input := "{}\n" + `{"a":1}` + "\r\n" + "\n" + `{"b":2}`

	var got []string
	runWSWith(t, input, func(_ Request, conn Conn) {
		for {
			msg, ok := conn.Read()
			if !ok {
				return
			}
			got = append(got, string(msg))
		}
	})
	want := []string{`{"a":1}`, ``, `{"b":2}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %q, want %q", got, want)
	}
}

// Writes reach stdout one JSON line each.
func TestRunWSWritesLines(t *testing.T) {
	out := runWSWith(t, "{}\n", func(_ Request, conn Conn) {
		conn.Write(map[string]int{"n": 1})
		conn.WriteRaw("raw")
	})
	if out != "{\"n\":1}\nraw\n" {
		t.Fatalf("stdout = %q", out)
	}
}
