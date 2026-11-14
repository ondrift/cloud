<?php
// The PHP program the loop cases run (loop_check.py).
//
// It serves one handler through \Drift\run, the entry point the generated PHP
// wrapper calls, so what the cases assert is what a slice's warm worker gets. It
// re-implements no loop.
//
// The handler reports what a function can see of its call: its pid, TMPDIR and
// HOME, and every DRIFT_SECRET_ variable. Two requests change what it does: a
// body with "print" writes that text to stdout, as a function's own echo does,
// and a body with "fail" throws.

require_once __DIR__ . '/../php/drift.php';

function handle($req) {
    $ask = is_array($req['body'] ?? null) ? $req['body'] : [];
    if (isset($ask['print'])) {
        echo $ask['print'], "\n";
    }
    if (!empty($ask['fail'])) {
        throw new \RuntimeException('loop conformance: the handler was asked to fail');
    }

    $secrets = [];
    foreach (getenv() as $name => $value) {
        if (strpos($name, 'DRIFT_SECRET_') === 0) {
            $secrets[$name] = $value;
        }
    }
    $tmpdir = getenv('TMPDIR');
    $home = getenv('HOME');
    return [
        'status' => 200,
        'message' => 'ok',
        'payload' => [
            'pid' => getmypid(),
            'tmpdir' => $tmpdir === false ? null : $tmpdir,
            'home' => $home === false ? null : $home,
            'secrets' => (object) $secrets,
        ],
    ];
}

\Drift\run('handle');
