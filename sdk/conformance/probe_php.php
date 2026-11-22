<?php
// The PHP program the start check cases run (probe_check.py).
//
// It hands a handler that must not run to the SDK's own entry points, \Drift\run
// for the entry "run" and \Drift\run_sse and \Drift\run_ws for the streaming
// ones. Its two arguments are the entry's name and the marker the handler writes
// to stderr, before it exits 97, if it is ever reached.

require_once __DIR__ . '/../php/drift.php';

$entries = ['run' => '\Drift\run', 'run_sse' => '\Drift\run_sse', 'run_ws' => '\Drift\run_ws'];
$entry = $argv[1] ?? '';
$marker = $argv[2] ?? '';
if (!isset($entries[$entry]) || $marker === '') {
    fwrite(STDERR, "usage: probe_php.php <run|run_sse|run_ws> <marker>\n");
    exit(2);
}

$entries[$entry](function (...$ignored) use ($marker) {
    fwrite(STDERR, $marker . "\n");
    exit(97);
});
