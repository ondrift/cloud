// The Node program the start check cases run (probe_check.py).
//
// It hands a handler that must not run to the SDK's own entry points, run for the
// entry "run" and runSSE and runWS for the streaming ones. Its two arguments are
// the entry's name and the marker the handler writes to stderr, before it exits
// 97, if it is ever reached.

"use strict";

const drift = require("../node/index.js");

const entries = { run: drift.run, run_sse: drift.runSSE, run_ws: drift.runWS };
const [entry, marker] = process.argv.slice(2);
if (!Object.hasOwn(entries, entry) || !marker) {
  process.stderr.write("usage: probe_node.js <run|run_sse|run_ws> <marker>\n");
  process.exit(2);
}

entries[entry](() => {
  require("fs").writeSync(2, `${marker}\n`);
  process.exit(97);
});
