// probe_test.js: a process started to be checked says hello and exits, and never
// reaches the handler.
//
// Run: node sdk/node/probe_test.js
//
// Before a deploy replaces the function that serves, the slice starts the new
// version once: DRIFT_INVOKE=probe1, stdin closed. It wants one line on stdout,
// {"hello":{"proto":"drift-probe","v":1}}, and exit status 0, with the handler
// never called, because a handler writes to databases and sends mail. The
// platform owns that contract; this holds the SDK's side of it.
//
// Answering means leaving the process, so each entry point is started as a
// process of its own. The handler in that process fails the test if it is
// called: it writes HANDLER_CALLED to stderr and exits 97. Each entry also runs
// without the check, with a request on stdin, where the handler IS reached;
// without that the absence of the marker could mean the guard cannot fire.

"use strict";

const { spawnSync } = require("child_process");
const path = require("path");

// The line the platform's start check reads, as the SDK repeats it.
const HELLO = '{"hello":{"proto":"drift-probe","v":1}}\n';
const CALLED = "HANDLER_CALLED";

// The slice allows a check ten seconds from spawn.
const WITHIN_MS = 10000;

// Every entry point the SDK has that reads a request.
const ENTRIES = ["run", "runSSE", "runWS"];

// What an entry reads when it is not being checked: a request, on one line.
const REQUEST = '{"path":"/","headers":{},"query":"","body":null}\n';

const PROGRAM = `
const drift = require(${JSON.stringify(path.join(__dirname, "index.js"))});
const handler = () => {
  require("fs").writeSync(2, "${CALLED}\\n");
  process.exit(97);
};
drift[process.argv[1]](handler);
`;

let failures = 0;
function check(what, ok, detail) {
  if (ok) {
    console.log(`  ✓ ${what}`);
    return;
  }
  console.log(`  ✗ ${what}${detail ? `: ${detail}` : ""}`);
  failures++;
}

// The entry point as its own process, as the slice starts a function. A request
// of null is a closed stdin (the null device, as the slice gives a check);
// otherwise it is written to stdin, which is then closed. Returns the finished
// process, or null when it was still running after WITHIN_MS.
function start(entry, invoke, request) {
  const env = { DRIFT_RUNTIME: "1" };
  if (invoke !== null) env.DRIFT_INVOKE = invoke;
  const done = spawnSync(process.execPath, ["-e", PROGRAM, entry], {
    env,
    input: request === null ? undefined : request,
    stdio: [request === null ? "ignore" : "pipe", "pipe", "pipe"],
    encoding: "utf8",
    timeout: WITHIN_MS,
  });
  return done.error || done.signal ? null : done;
}

// The last line of what a process wrote to stderr: where a failure says why.
function tail(text) {
  const lines = text.trim().split("\n");
  const last = lines[lines.length - 1];
  return last.length <= 160 ? last : `...${last.slice(-160)}`;
}

const drift = require("./index.js");

console.log("Node SDK: the start check");

check("PROBE_PROTOCOL is 1", drift.PROBE_PROTOCOL === 1, String(drift.PROBE_PROTOCOL));

for (const entry of ENTRIES) {
  const done = start(entry, "probe1", null);
  if (done === null) {
    check(`${entry} answers within ${WITHIN_MS / 1000}s`, false, "it was still running");
    continue;
  }
  const detail = `stdout ${JSON.stringify(done.stdout)}, status ${done.status}, stderr ${JSON.stringify(tail(done.stderr))}`;
  check(`${entry} writes the hello line and nothing else`, done.stdout === HELLO, detail);
  check(`${entry} exits 0`, done.status === 0, detail);
  check(`${entry} does not reach the handler`, !done.stderr.includes(CALLED), detail);
}

// THE CONTROLS. Without the check an entry reads its request and calls the
// handler, so the marker above can appear, and an SDK that answered every start
// as a check would show here.
for (const [invoke, what] of [
  [null, "without the check"],
  ["probe2", "under another version of the check"],
]) {
  for (const entry of ENTRIES) {
    const done = start(entry, invoke, REQUEST);
    if (done === null) {
      check(`${entry} ${what} reaches the handler`, false, `it was still running after ${WITHIN_MS / 1000}s`);
      continue;
    }
    check(
      `${entry} ${what} reaches the handler and says no hello`,
      done.stderr.includes(CALLED) && done.status === 97 && !done.stdout.includes("drift-probe"),
      `stdout ${JSON.stringify(done.stdout)}, status ${done.status}, stderr ${JSON.stringify(tail(done.stderr))}`
    );
  }
}

console.log(failures === 0 ? "\n✅ every case passed" : `\n❌ ${failures} assertion(s) failed`);
process.exit(failures === 0 ? 0 : 1);
