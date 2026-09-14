<?php
// status_test.php — a non-2xx from Backbone is an ERROR, not a value.
//
// Run: php sdk/php/status_test.php
//
// # Why this is not in sdk/conformance
//
// The conformance harness runs every case against the LOCAL store — no
// BACKBONE_URL, no slice — because that is the loop a developer meets first and
// the one where the six SDKs had drifted. This defect is the mirror: it is
// correct locally and wrong on the wire, so nothing the harness runs could ever
// see it.
//
// So this stands up a REAL HTTP server on loopback, points BACKBONE_URL at it,
// and asks what `_call` does with a 404. Nothing is stubbed between the status
// line and the assertion.
//
// The same gap exists for Rust (`blob::get` maps every transport failure and a
// genuine 404 onto one `None`) and is NOT covered here — see the note in the
// PR that added this file. The real fix is a second conformance mode that runs
// the nine cases against a live Backbone; this is the one-language stopgap for
// the defect that was actually found.

require_once __DIR__ . '/drift.php';

$failures = 0;

function check(string $what, bool $ok, string $detail = ''): void {
    global $failures;
    if ($ok) {
        echo "  ✓ $what\n";
        return;
    }
    echo "  ✗ $what" . ($detail !== '' ? ": $detail" : "") . "\n";
    $failures++;
}

// ── a server that refuses ────────────────────────────────────────────────────
//
// PHP's built-in server, on a port the OS picks, serving one router script that
// answers whatever status the path asks for. A real socket, a real status line,
// a real body.
$router = tempnam(sys_get_temp_dir(), 'drift-router-') . '.php';
file_put_contents($router, <<<'ROUTER'
<?php
// /status/<code> answers that code with a body naming it.
if (preg_match('#^/status/(\d+)#', $_SERVER['REQUEST_URI'], $m)) {
    http_response_code((int) $m[1]);
    echo "the store refused this: " . $m[1];
    return true;
}
http_response_code(200);
echo json_encode(["ok" => true]);
return true;
ROUTER);

$port = 8731;
$descriptors = [0 => ['pipe', 'r'], 1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w']];
$proc = proc_open(
    sprintf('exec php -S 127.0.0.1:%d %s', $port, escapeshellarg($router)),
    $descriptors, $pipes
);
if (!is_resource($proc)) {
    fwrite(STDERR, "could not start the test server\n");
    exit(1);
}

// Wait for it to accept, rather than sleeping a guess.
$up = false;
for ($i = 0; $i < 100; $i++) {
    $s = @fsockopen('127.0.0.1', $port, $errno, $errstr, 0.1);
    if ($s) { fclose($s); $up = true; break; }
    usleep(50000);
}
if (!$up) {
    proc_terminate($proc);
    fwrite(STDERR, "the test server never accepted a connection\n");
    exit(1);
}

putenv("BACKBONE_URL=http://127.0.0.1:$port");

echo "PHP SDK — a non-2xx is an error, not a value\n";

// ── THE test ────────────────────────────────────────────────────────────────
//
// Before the fix, `_call` read $status and never consulted it again: this
// returned the string "the store refused this: 404" as though it were the
// value, and a caller's `if ($v !== '')` took the wrong branch holding an error
// message.
foreach ([400, 401, 404, 409, 500] as $code) {
    $threw = false;
    $returned = null;
    try {
        $returned = \Drift\_call('GET', "status/$code");
    } catch (\Drift\BackboneError $e) {
        $threw = true;
        check("$code names the path", str_contains($e->getMessage(), "status/$code"),
            $e->getMessage());
        check("$code carries the body", str_contains($e->getMessage(), "the store refused this"),
            $e->getMessage());
    }
    check("$code throws rather than returning a body", $threw,
        $threw ? '' : 'returned ' . var_export($returned, true));
}

// ── the control ─────────────────────────────────────────────────────────────
//
// Without this the assertions above would pass on a `_call` that threw for
// EVERYTHING, which would be a different and equally broken SDK.
$ok = null;
$threw = false;
try {
    $ok = \Drift\_call('GET', 'anything');
} catch (\Throwable $e) {
    $threw = true;
}
check('a 200 still returns its decoded body', !$threw && is_array($ok) && ($ok['ok'] ?? false) === true,
    $threw ? 'it threw' : var_export($ok, true));

proc_terminate($proc);
proc_close($proc);
@unlink($router);

echo $failures === 0
    ? "\n✅ every case passed\n"
    : "\n❌ $failures assertion(s) failed\n";
exit($failures === 0 ? 0 : 1);
