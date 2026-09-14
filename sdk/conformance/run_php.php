<?php
// Drives the PHP SDK through the shared conformance cases and prints what it
// got, as JSON, for check.py to compare against expected.json.
//
// It asserts NOTHING itself. The assertions are in expected.json, once, for all
// six languages — six independently-worded suites can each pass while asserting
// subtly different things, and that is how five SDKs came to disagree with Go
// about local blobs without anybody noticing.

// No BACKBONE_URL: every case runs against the in-memory local store, which is
// the loop a developer meets first and the one where the six drifted. Set BEFORE
// the require, because the SDK caches the value on first read.
putenv('BACKBONE_URL=');
$_ENV['BACKBONE_URL'] = '';

require_once __DIR__ . '/../php/drift.php';

$out = [];

function emit(): void {
    global $out;
    echo json_encode($out, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES), "\n";
}

// Report the first thing that went wrong and stop. A driver that carried on
// would report later cases against a store in a state nobody intended.
function fail_with(string $message): void {
    global $out;
    $out['error'] = $message;
    emit();
    exit(0); // check.py turns the reported error into the failure
}

// -- Blob --------------------------------------------------------------------
try {
    \Drift\Backbone\Blob::put('uploads/greeting.txt', 'hello, slice', 'text/plain');
} catch (\Throwable $e) {
    fail_with('Blob::put with a bucket: ' . $e->getMessage());
}
try {
    $out['blob_with_bucket'] = \Drift\Backbone\Blob::get('uploads/greeting.txt');
} catch (\Throwable $e) {
    fail_with('Blob::get with a bucket: ' . $e->getMessage());
}

try {
    \Drift\Backbone\Blob::put('greeting.txt', 'bare');
} catch (\Throwable $e) {
    fail_with('Blob::put without a bucket: ' . $e->getMessage());
}
try {
    $out['blob_without_bucket'] = \Drift\Backbone\Blob::get('greeting.txt');
} catch (\Throwable $e) {
    fail_with('Blob::get without a bucket: ' . $e->getMessage());
}

// A key never written must FAIL, and "error" is what a failure reports here. An
// SDK that returns empty-with-no-error reports null instead and fails the case —
// which is the whole point, because an implementation returning empty for
// EVERYTHING would otherwise pass the two cases above by accident.
try {
    $got = \Drift\Backbone\Blob::get('uploads/never-written.txt');
    $out['blob_absent'] = ($got === null || $got === '') ? null : $got;
} catch (\Throwable $e) {
    $out['blob_absent'] = 'error';
}

// -- Ordered NoSQL reads -----------------------------------------------------
$small = \Drift\Backbone\Nosql::collection('conformance_small');
for ($i = 1; $i <= 12; $i++) {
    try {
        $small->insert(['n' => $i]);
    } catch (\Throwable $e) {
        fail_with("Insert $i: " . $e->getMessage());
    }
}

try {
    $out['list_in_order'] = array_map(fn($d) => $d['n'], $small->list_in_order(100));
} catch (\Throwable $e) {
    fail_with('list_in_order: ' . $e->getMessage());
}
try {
    // The storage key is an ARRAY key in the store and PHP stores a numeric-looking
    // one as an int, so the `_key` field is what carries the string the platform
    // sends. Cast, or the JSON reports numbers where every other language reports
    // strings and the case fails for a reason that is not about ordering.
    $out['list_default_order'] = array_map(fn($d) => (string) $d['_key'], $small->list());
} catch (\Throwable $e) {
    fail_with('list: ' . $e->getMessage());
}

// Past the 1000-row page cap, which is where a cursor that advances in KEY order
// rather than in the ordered sequence silently truncates.
$big = \Drift\Backbone\Nosql::collection('conformance_big');
for ($i = 1; $i <= 1200; $i++) {
    try {
        $big->insert(['n' => $i]);
    } catch (\Throwable $e) {
        fail_with("Insert big $i: " . $e->getMessage());
    }
}
try {
    $ns = array_map(fn($d) => $d['n'], $big->list_all_in_order());
    $out['list_all_in_order_count'] = count($ns);
    if (count($ns) > 0) {
        $out['list_all_in_order_first'] = $ns[0];
        $out['list_all_in_order_last'] = $ns[count($ns) - 1];
    }
    if (count($ns) >= 1002) {
        $out['list_all_in_order_boundary'] = array_slice($ns, 998, 4);
    }
} catch (\Throwable $e) {
    fail_with('list_all_in_order: ' . $e->getMessage());
}

emit();
