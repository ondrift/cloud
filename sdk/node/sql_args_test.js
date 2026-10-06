// sql_args_test.js — a SQL statement always sends `args` as a JSON array.
//
// Run: node sdk/node/sql_args_test.js
//
// A slice reads `args` as a list and refuses `"args":null` as invalid JSON, so
// a statement that sends null fails before it reaches SQLite. The conformance
// harness runs against the local store and never serialises a request, so this
// stands up a real HTTP server on loopback, points BACKBONE_URL at it, and
// reads the bodies the SDK actually sends.

"use strict";

const http = require("http");

let failures = 0;
function check(what, ok, detail) {
  if (ok) {
    console.log(`  ✓ ${what}`);
    return;
  }
  console.log(`  ✗ ${what}${detail ? `: ${detail}` : ""}`);
  failures++;
}

const bodies = [];
const server = http.createServer((req, res) => {
  let raw = "";
  req.on("data", (c) => (raw += c));
  req.on("end", () => {
    bodies.push({ path: req.url, raw });
    res.setHeader("Content-Type", "application/json");
    if (req.url === "/sql/query") res.end(JSON.stringify({ columns: ["n"], rows: [[1]] }));
    else if (req.url === "/sql/execute") res.end(JSON.stringify({ rows_affected: 0, last_insert_id: 0 }));
    else if (req.url === "/sql/begin") res.end(JSON.stringify({ tx: "t1" }));
    else res.end("{}");
  });
});

// The raw `args` text of the last body sent to path, as the slice parses it.
function argsOf(path) {
  const hit = bodies.filter((b) => b.path === path).pop();
  if (!hit) return "(no request)";
  return JSON.stringify(JSON.parse(hit.raw).args);
}

server.listen(0, "127.0.0.1", async () => {
  process.env.BACKBONE_URL = `http://127.0.0.1:${server.address().port}`;
  const drift = require("./index.js");
  const db = drift.backbone.sql("main");

  console.log("Node SDK — a SQL statement always sends an args array");

  try {
    for (const [label, call] of [
      ["no args", (f) => f("SELECT 1")],
      ["args null", (f) => f("SELECT 1", null)],
      ["args undefined", (f) => f("SELECT 1", undefined)],
    ]) {
      await call((s, a) => db.query(s, a));
      check(`query with ${label} sends []`, argsOf("/sql/query") === "[]", argsOf("/sql/query"));
      await call((s, a) => db.execute(s, a));
      check(`execute with ${label} sends []`, argsOf("/sql/execute") === "[]", argsOf("/sql/execute"));
    }

    const tx = await db.begin();
    await tx.query("SELECT 1", null);
    check("tx.query with args null sends []", argsOf("/sql/query") === "[]", argsOf("/sql/query"));
    await tx.execute("DELETE FROM t");
    check("tx.execute with no args sends []", argsOf("/sql/execute") === "[]", argsOf("/sql/execute"));
    const last = JSON.parse(bodies.filter((b) => b.path === "/sql/execute").pop().raw);
    check("a transaction keeps its token", last.tx === "t1", JSON.stringify(last.tx));

    // THE CONTROL: arguments that are passed arrive unchanged and in order.
    await db.query("SELECT ?, ?", ["a", 2]);
    check("passed args arrive in order", argsOf("/sql/query") === '["a",2]', argsOf("/sql/query"));
  } catch (e) {
    check("no call threw", false, String(e));
  }

  server.close();
  console.log(failures === 0 ? "\n✅ every case passed" : `\n❌ ${failures} assertion(s) failed`);
  process.exit(failures === 0 ? 0 : 1);
});
