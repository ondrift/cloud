package genmain

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	drift "github.com/ondrift/cloud/sdk/go"
)

// serveLegacy is the sequential loop: the slice writes newline-delimited
// envelopes and reads one response line per call; stdin closing retires the
// process. It is what an older slice, a warm worker at concurrency 1 and every
// one-shot spawn (a trigger's, or a cron's) get, and its response lines are the
// bytes the CLI's native templates write for the same envelopes.
//
// Per call, as those templates do: TMPDIR and HOME point at the envelope's
// scratch directory, and the envelope's secrets replace the previous call's.
// The context carries the call's runtime budget.
//
// A handler panic is not recovered: the process dies, as it does under the
// native templates, and the slice reports how.
func serveLegacy(in io.Reader, out io.Writer, handler handlerFunc) {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	secrets := newSecretState()
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return // stdin closed or malformed: this worker is done
		}

		var req drift.Request
		_ = json.Unmarshal(raw, &req)

		var extras envelopeExtras
		_ = json.Unmarshal(raw, &extras)
		if extras.Tmpdir != "" && filepath.IsAbs(extras.Tmpdir) {
			_ = os.Setenv("TMPDIR", extras.Tmpdir)
			_ = os.Setenv("HOME", extras.Tmpdir)
		}
		secrets.apply(extras.Secrets, extras.Previous)

		ctx, cancel := context.WithTimeout(context.Background(), runtimeBudget(req.Headers))
		resp := handler(ctx, req)
		cancel()

		reply := map[string]any{
			"status":  resp.Status,
			"message": resp.Message,
			"payload": payloadOrNull(resp.Payload),
		}
		if len(resp.Headers) > 0 {
			reply["headers"] = resp.Headers
		}
		// The process's own high-water mark: nothing exits for the slice to
		// weigh with wait4, so a warm worker reports it, and the slice reads it
		// when it cannot read the process itself.
		if rss := peakRSSBytes(); rss > 0 {
			reply["rss_bytes"] = rss
		}
		if err := enc.Encode(reply); err != nil {
			return
		}
	}
}

// payloadOrNull is a handler's payload as it goes on the wire: JSON null for
// none, which is what json.Marshal of a nil payload gives the templates.
func payloadOrNull(p json.RawMessage) json.RawMessage {
	if len(p) == 0 {
		return json.RawMessage("null")
	}
	return p
}
