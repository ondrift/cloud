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
//
// DRIFT_CONFORMANCE_BACKBONE_URL names a running slice's Backbone instead, and
// runs the queue cases against it alone: the slice is the authority on the queue
// wire, and the local store is held to what it answers.
$slice = (string) getenv('DRIFT_CONFORMANCE_BACKBONE_URL');
putenv('BACKBONE_URL=' . $slice);
$_ENV['BACKBONE_URL'] = $slice;

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

// -- Queue -------------------------------------------------------------------
// Every queue call once. The names carry the time, so a run against a slice,
// whose queues outlive the run, never meets an earlier run's.
function attempt(callable $fn, callable $read) {
    try {
        return $read($fn());
    } catch (\Throwable $e) {
        return 'error';
    }
}

$stamp = hrtime(true) . getmypid();
$q = \Drift\Backbone\queue("conformance-php-$stamp");
$absent = \Drift\Backbone\queue("conformance-absent-php-$stamp");
$body = fn($msg) => $msg === null ? null : $msg['n'];
$same = fn($v) => $v;
$refused = fn($_) => null;

$out['queue_pop_never_pushed'] = attempt(fn() => $absent->pop(), $body);
try {
    $absent->pop();
    $out['queue_error_carries_the_refusal'] = false;
} catch (\Throwable $e) {
    $out['queue_error_carries_the_refusal'] =
        str_contains($e->getMessage(), 'HTTP 404') && str_contains($e->getMessage(), 'not found');
}
$out['queue_len_never_pushed'] = attempt(fn() => $absent->len(), $same);
$out['queue_push_not_an_object'] = attempt(fn() => $q->push('a bare string'), $refused);
$out['queue_push_bad_name'] = attempt(fn() => \Drift\Backbone\queue('-flag')->push(['n' => 0]), $refused);
$out['queue_push_ids'] = [];
for ($n = 1; $n <= 3; $n++) {
    try {
        $out['queue_push_ids'][] = $q->push(['n' => $n]);
    } catch (\Throwable $e) {
        fail_with("queue->push $n: " . $e->getMessage());
    }
}
$out['queue_len_after_push'] = attempt(fn() => $q->len(), $same);
$out['queue_peek'] = attempt(fn() => $q->peek(), $body);
$out['queue_len_after_peek'] = attempt(fn() => $q->len(), $same);
$out['queue_pop_in_order'] = [];
for ($i = 0; $i < 3; $i++) {
    $out['queue_pop_in_order'][] = attempt(fn() => $q->pop(), $body);
}
$out['queue_pop_empty'] = attempt(fn() => $q->pop(), $body);
$out['queue_peek_empty'] = attempt(fn() => $q->peek(), $body);
$out['queue_len_after_pop'] = attempt(fn() => $q->len(), $same);

if ($slice !== '') {
    emit();
    exit(0);
}

// A declared queue, named the way the CLI names it for a local run.
putenv('DRIFT_DECLARED_QUEUES=conformance-declared');
$out['declared_queue_pop_empty'] = attempt(fn() => \Drift\Backbone\queue('conformance-declared')->pop(), $same);

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

// -- Filtered NoSQL reads ----------------------------------------------------
$filtered = \Drift\Backbone\Nosql::collection('conformance_filter');
$wide = ['_id' => 'wide'];
for ($i = 0; $i < 9; $i++) $wide["f$i"] = 'v';
foreach ([
    ['_id' => 'x1', 'a' => '1', 'b' => '1'],
    ['_id' => 'x2', 'a' => '1', 'b' => '2'],
    ['_id' => 'x3', 'a' => '2', 'b' => '1'],
    ['_id' => 'x4', 'a' => '3', 'b' => '2'],
    $wide,
] as $doc) {
    try {
        $filtered->insert($doc);
    } catch (\Throwable $e) {
        fail_with("Insert {$doc['_id']}: " . $e->getMessage());
    }
}
$ids_of = static function (array $rows): array {
    $ids = array_map(fn($d) => $d['_id'], $rows);
    sort($ids, SORT_STRING);
    return $ids;
};
try {
    $out['list_filter_two_fields'] = $ids_of($filtered->list(['a' => '1', 'b' => '2']));
} catch (\Throwable $e) {
    fail_with('list with two entries: ' . $e->getMessage());
}
try {
    $out['list_filter_one_field'] = $ids_of($filtered->list(['a' => '1']));
} catch (\Throwable $e) {
    fail_with('list with one entry: ' . $e->getMessage());
}
try {
    $nine = [];
    for ($i = 0; $i < 9; $i++) $nine["f$i"] = 'v';
    $out['list_filter_over_cap'] = $ids_of($filtered->list($nine));
} catch (\Throwable $e) {
    $out['list_filter_over_cap'] = 'error';
}

// -- Edge links --------------------------------------------------------------
// A loopback peer that echoes the path it was asked for and the identity header
// it received, null when the header is absent. PHP serves one request at a time,
// so the peer is PHP's built-in server in its own process, on a port the OS
// picks.
$probe = stream_socket_server('tcp://127.0.0.1:0');
$port = (int) substr(strrchr(stream_socket_get_name($probe, false), ':'), 1);
fclose($probe);
$router = tempnam(sys_get_temp_dir(), 'drift-peer-') . '.php';
file_put_contents($router, <<<'ROUTER'
<?php
header('Content-Type: application/json');
echo json_encode([
    'path' => parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH),
    'slice' => $_SERVER['HTTP_X_DRIFT_SLICE'] ?? null,
], JSON_UNESCAPED_SLASHES);
return true;
ROUTER);
$peer = proc_open(
    sprintf('exec %s -S 127.0.0.1:%d %s', escapeshellarg(PHP_BINARY), $port, escapeshellarg($router)),
    [0 => ['pipe', 'r'], 1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w']],
    $pipes
);
$up = false;
for ($i = 0; $i < 100 && is_resource($peer); $i++) {
    $s = @fsockopen('127.0.0.1', $port, $errno, $errstr, 0.1);
    if ($s) { fclose($s); $up = true; break; }
    usleep(50000);
}
if (!$up) fail_with('the loopback peer never accepted a connection');

putenv('DRIFT_SLICE');
putenv("DRIFT_LINK_BILLING_API_URL=http://127.0.0.1:$port/base/");
$echoed = static fn(array $resp): array => json_decode($resp['body'], true) ?? [];

try {
    $e = $echoed(\Drift\Edge\slice('billing-api')->get('/api/events'));
    $out['edge_slice_url'] = $e['path'] ?? null;
    $out['slice_identity_absent'] = $e['slice'] ?? null;
} catch (\Throwable $e) {
    fail_with('Edge\slice get: ' . $e->getMessage());
}
try {
    $out['slice_url'] = $echoed(\Drift\slice('billing-api')->get('/api/events'))['path'] ?? null;
} catch (\Throwable $e) {
    fail_with('slice get: ' . $e->getMessage());
}

putenv('DRIFT_SLICE=billing');
try {
    $resp = \Drift\Edge\slice('billing-api')->request('GET', '/api/events', ['X-Drift-Slice' => 'operator']);
    $out['slice_identity_kept'] = $echoed($resp)['slice'] ?? null;
} catch (\Throwable $e) {
    fail_with('Edge\slice request with a caller identity: ' . $e->getMessage());
}
putenv('DRIFT_SLICE');
proc_terminate($peer);
proc_close($peer);
@unlink($router);

try {
    \Drift\Edge\slice('billing')->get('/x');
    $out['edge_slice_not_linked'] = null;
} catch (\Throwable $e) {
    $out['edge_slice_not_linked'] = $e->getMessage();
}
try {
    \Drift\slice('billing')->get('/x');
    $out['slice_not_linked'] = null;
} catch (\Throwable $e) {
    $out['slice_not_linked'] = $e->getMessage();
}

emit();
