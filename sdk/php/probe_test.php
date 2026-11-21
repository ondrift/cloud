<?php
// probe_test.php: a process started to be checked says hello and exits, and never
// reaches the handler.
//
// Run: php sdk/php/probe_test.php
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

// The line the platform's start check reads, as the SDK repeats it.
const HELLO = "{\"hello\":{\"proto\":\"drift-probe\",\"v\":1}}\n";
const CALLED = 'HANDLER_CALLED';

// The slice allows a check ten seconds from spawn.
const WITHIN = 10.0;

// Every entry point the SDK has that reads a request.
const ENTRIES = ['run', 'run_sse', 'run_ws'];

// What an entry reads when it is not being checked: a request, on one line.
const REQUEST = "{\"path\":\"/\",\"headers\":{},\"query\":\"\",\"body\":null}\n";

const PROGRAM = <<<'PHP'
<?php
require_once %s;

function handle(...$ignored) {
    fwrite(STDERR, "HANDLER_CALLED\n");
    exit(97);
}

$entry = ['run' => '\Drift\run', 'run_sse' => '\Drift\run_sse', 'run_ws' => '\Drift\run_ws'][$argv[1]];
$entry('handle');
PHP;

/** A directory for the test's own files, and the program written into it. */
function workdir(): string {
    $dir = sys_get_temp_dir() . '/drift-probe-test-' . getmypid() . '-' . bin2hex(random_bytes(4));
    mkdir($dir, 0700, true);
    file_put_contents($dir . '/program.php', sprintf(PROGRAM, var_export(__DIR__ . '/drift.php', true)));
    return $dir;
}

function remove_tree(string $path): void {
    if (is_link($path) || is_file($path)) {
        @unlink($path);
        return;
    }
    foreach (@scandir($path) ?: [] as $entry) {
        if ($entry !== '.' && $entry !== '..') remove_tree("$path/$entry");
    }
    @rmdir($path);
}

/**
 * The entry point as its own process, as the slice starts a function. A request
 * of null is a closed stdin (the null device, as the slice gives a check);
 * otherwise it is written to stdin, which is then closed. Returns the finished
 * process as [stdout, stderr, exit status], or null when it was still running
 * after WITHIN seconds.
 */
function start(string $program, string $entry, ?string $invoke, ?string $request): ?array {
    $env = ['DRIFT_RUNTIME' => '1'];
    if ($invoke !== null) {
        $env['DRIFT_INVOKE'] = $invoke;
    }
    $proc = proc_open(
        [PHP_BINARY, '-n', $program, $entry],
        [0 => $request === null ? ['file', '/dev/null', 'r'] : ['pipe', 'r'], 1 => ['pipe', 'w'], 2 => ['pipe', 'w']],
        $pipes,
        null,
        $env
    );
    if (!is_resource($proc)) {
        fwrite(STDERR, "could not start the program\n");
        exit(1);
    }
    if ($request !== null) {
        fwrite($pipes[0], $request);
        fclose($pipes[0]);
    }
    stream_set_blocking($pipes[1], false);
    stream_set_blocking($pipes[2], false);

    $out = $err = '';
    $deadline = microtime(true) + WITHIN;
    while (true) {
        $out .= (string) stream_get_contents($pipes[1]);
        $err .= (string) stream_get_contents($pipes[2]);
        // The exit status is valid only on the first read after the process ends.
        $status = proc_get_status($proc);
        if (!$status['running']) {
            $out .= (string) stream_get_contents($pipes[1]);
            $err .= (string) stream_get_contents($pipes[2]);
            proc_close($proc);
            return [$out, $err, $status['exitcode']];
        }
        if (microtime(true) > $deadline) {
            proc_terminate($proc, 9);
            proc_close($proc);
            return null;
        }
        usleep(5000);
    }
}

/** The last line of what a process wrote to stderr: where a failure says why. */
function tail(string $text): string {
    $lines = preg_split('/\R/', trim($text));
    $last = (string) end($lines);
    return strlen($last) <= 160 ? $last : '...' . substr($last, -160);
}

function describe(array $done): string {
    return 'stdout ' . json_encode($done[0]) . ', status ' . $done[2] . ', stderr ' . json_encode(tail($done[1]));
}

echo "PHP SDK: the start check\n";

$version = defined('\\Drift\\PROBE_PROTOCOL') ? \constant('\\Drift\\PROBE_PROTOCOL') : null;
check('\\Drift\\PROBE_PROTOCOL is 1', $version === 1, var_export($version, true));

$dir = workdir();
$program = $dir . '/program.php';

foreach (ENTRIES as $entry) {
    $done = start($program, $entry, 'probe1', null);
    if ($done === null) {
        check("$entry answers within " . (int) WITHIN . 's', false, 'it was still running');
        continue;
    }
    check("$entry writes the hello line and nothing else", $done[0] === HELLO, describe($done));
    check("$entry exits 0", $done[2] === 0, describe($done));
    check("$entry does not reach the handler", !str_contains($done[1], CALLED), describe($done));
}

// THE CONTROLS. Without the check an entry reads its request and calls the
// handler, so the marker above can appear, and an SDK that answered every start
// as a check would show here.
foreach ([[null, 'without the check'], ['probe2', 'under another version of the check']] as [$invoke, $what]) {
    foreach (ENTRIES as $entry) {
        $done = start($program, $entry, $invoke, REQUEST);
        if ($done === null) {
            check("$entry $what reaches the handler", false, 'it was still running after ' . (int) WITHIN . 's');
            continue;
        }
        check(
            "$entry $what reaches the handler and says no hello",
            str_contains($done[1], CALLED) && $done[2] === 97 && !str_contains($done[0], 'drift-probe'),
            describe($done)
        );
    }
}

remove_tree($dir);

echo $failures === 0
    ? "\n✅ every case passed\n"
    : "\n❌ $failures assertion(s) failed\n";
exit($failures === 0 ? 0 : 1);
