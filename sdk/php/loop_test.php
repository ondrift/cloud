<?php
// loop_test.php — \Drift\run under DRIFT_RUNTIME serves many calls from one process.
//
// Run: php sdk/php/loop_test.php
//
// The program is a small handler served through \Drift\run, started as a slice
// starts a warm worker: DRIFT_RUNTIME set, nothing else in its environment,
// envelopes written one per line to its stdin, one reply line read back for
// each. The same conversation, for every SDK, is held by sdk/conformance; this
// file is PHP's own, so a PHP change fails here before the conformance job gets
// to it, and it holds what the shared cases cannot say in one language-neutral
// way: what PHP does with a handler's echo, its diagnostics, its writes to the
// STDOUT constant and its scratch directory.

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

/** What a test saw, recorded without asserting it. */
function observe(string $what): void {
    echo "  · $what\n";
}

// How long a worker has to answer an envelope or to end after its input does.
const WAIT = 5.0;

// ── the program the worker runs ─────────────────────────────────────────────
//
// What a request asks the handler to do is in its body. The reply reports what
// a function can see of its call: pid, TMPDIR and HOME as getenv, $_ENV and
// $_SERVER each read them, every DRIFT_SECRET_ variable the same three ways,
// and the directory sys_get_temp_dir() names.
const PROGRAM = <<<'PHP'
<?php
require_once %s;

function handle($req) {
    $ask = is_array($req['body'] ?? null) ? $req['body'] : [];
    if (isset($ask['echo'])) echo $ask['echo'];
    if (isset($ask['print'])) print $ask['print'];
    if (!empty($ask['warn'])) echo $undefined_on_purpose;
    if (!empty($ask['notice'])) trigger_error('loop-test-notice', E_USER_NOTICE);
    if (isset($ask['error_log'])) error_log($ask['error_log']);
    if (isset($ask['stdout'])) fwrite(STDOUT, $ask['stdout']);
    if (!empty($ask['fail'])) throw new RuntimeException('loop-test-failure');

    $secrets = function (array $from): array {
        $found = [];
        foreach ($from as $name => $value) {
            if (strpos((string) $name, 'DRIFT_SECRET_') === 0) $found[$name] = $value;
        }
        ksort($found);
        return $found;
    };
    $temp = sys_get_temp_dir();
    $payload = [
        'pid' => getmypid(),
        'tmpdir' => ['getenv' => getenv('TMPDIR'), 'env' => $_ENV['TMPDIR'] ?? null, 'server' => $_SERVER['TMPDIR'] ?? null],
        'home' => ['getenv' => getenv('HOME'), 'env' => $_ENV['HOME'] ?? null, 'server' => $_SERVER['HOME'] ?? null],
        'secrets' => ['getenv' => (object) $secrets(getenv()), 'env' => (object) $secrets($_ENV), 'server' => (object) $secrets($_SERVER)],
        'temp' => $temp,
    ];
    if (isset($ask['write'])) {
        file_put_contents($temp . '/' . $ask['write'], 'written');
    }
    if (isset($ask['look'])) {
        $payload['seen'] = is_file($temp . '/' . $ask['look']);
    }
    if (!empty($ask['scratch'])) {
        $name = tempnam(sys_get_temp_dir(), 'loop');
        $handle = tmpfile();
        $payload['tempnam'] = $name === false ? false : dirname($name);
        $payload['tmpfile'] = $handle === false ? false : dirname(stream_get_meta_data($handle)['uri']);
        if ($name !== false) unlink($name);
    }
    return ['status' => 200, 'message' => 'ok', 'payload' => $payload];
}

\Drift\run('handle');
PHP;

/** A directory for the test's own files, and the program written into it. */
function workdir(): string {
    $dir = sys_get_temp_dir() . '/drift-loop-test-' . getmypid() . '-' . bin2hex(random_bytes(4));
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

/** One program started as a slice starts a worker, and the pipes to it. */
final class Worker {
    /** @var resource */
    private $proc;
    /** @var resource[] */
    private array $pipes = [];
    private string $out = '';
    private string $err = '';
    private bool $eof = false;
    private bool $errClosed = false;
    private ?int $status = null;
    public int $pid = 0;

    public function __construct(string $program, array $env = ['DRIFT_RUNTIME' => '1']) {
        $this->proc = proc_open(
            [PHP_BINARY, '-n', $program],
            [0 => ['pipe', 'r'], 1 => ['pipe', 'w'], 2 => ['pipe', 'w']],
            $this->pipes,
            null,
            $env
        );
        if (!is_resource($this->proc)) {
            fwrite(STDERR, "could not start the worker\n");
            exit(1);
        }
        stream_set_blocking($this->pipes[1], false);
        stream_set_blocking($this->pipes[2], false);
        $this->pid = (int) proc_get_status($this->proc)['pid'];
    }

    public function send(string $data): void {
        @fwrite($this->pipes[0], $data);
        @fflush($this->pipes[0]);
    }

    public function closeStdin(): void {
        if (isset($this->pipes[0]) && is_resource($this->pipes[0])) fclose($this->pipes[0]);
    }

    private function fill(float $deadline): bool {
        $left = $deadline - microtime(true);
        if ($left <= 0) return false;
        $read = [];
        if (!$this->eof) $read[] = $this->pipes[1];
        if (!$this->errClosed) $read[] = $this->pipes[2];
        if ($read === []) return false;
        $write = $except = null;
        $ready = @stream_select($read, $write, $except, (int) $left, (int) (fmod($left, 1) * 1e6));
        if (!$ready) return false;
        foreach ($read as $stream) {
            $chunk = fread($stream, 65536);
            $got = $chunk !== '' && $chunk !== false;
            if ($stream === $this->pipes[1]) {
                if ($got) {
                    $this->out .= $chunk;
                } elseif (feof($stream)) {
                    $this->eof = true;
                }
            } elseif ($got) {
                $this->err .= $chunk;
            } elseif (feof($stream)) {
                $this->errClosed = true;
            }
        }
        return true;
    }

    /** The next complete line from stdout, null at its end or after the timeout. */
    public function line(float $timeout = WAIT): ?string {
        $deadline = microtime(true) + $timeout;
        while (true) {
            $end = strpos($this->out, "\n");
            if ($end !== false) {
                $line = substr($this->out, 0, $end + 1);
                $this->out = (string) substr($this->out, $end + 1);
                return $line;
            }
            if ($this->eof || !$this->fill($deadline)) return null;
        }
    }

    /** Everything still to come on stdout until it closes, and whether it did. */
    public function rest(float $timeout = WAIT): array {
        $deadline = microtime(true) + $timeout;
        while (!$this->eof && $this->fill($deadline)) {
        }
        $data = $this->out;
        $this->out = '';
        return [$data, $this->eof];
    }

    /** The exit status, null while it still runs after the timeout. */
    public function exit(float $timeout = 1.0): ?int {
        $deadline = microtime(true) + $timeout;
        do {
            $status = proc_get_status($this->proc);
            if (!$status['running']) {
                if ($this->status === null) $this->status = $status['exitcode'];
                return $this->status;
            }
            usleep(10000);
        } while (microtime(true) < $deadline);
        return null;
    }

    public function stderr(): string {
        // Whatever the worker has written so far: read until nothing more is ready.
        while ($this->fill(microtime(true) + 0.05)) {
        }
        return $this->err;
    }

    public function stop(): void {
        $this->closeStdin();
        if (proc_get_status($this->proc)['running']) proc_terminate($this->proc, 9);
        foreach ([1, 2] as $i) {
            if (is_resource($this->pipes[$i])) fclose($this->pipes[$i]);
        }
        proc_close($this->proc);
    }
}

function envelope(array $call, int $number): string {
    $out = ['path' => "/loop/$number", 'headers' => (object) [], 'query' => '', 'body' => (object) ($call['body'] ?? [])];
    foreach (['tmpdir', 'secrets', 'secrets_previous'] as $member) {
        if (array_key_exists($member, $call)) $out[$member] = $call[$member];
    }
    return json_encode($out);
}

/** The reply a line holds, or null when it is not a reply. */
function reply(?string $line): ?array {
    if ($line === null) return null;
    $decoded = json_decode(trim($line), true);
    return is_array($decoded) && ($decoded['status'] ?? null) === 200 && is_array($decoded['payload'] ?? null) ? $decoded : null;
}

/** Send each call and read its reply before sending the next, as a slice does. */
function converse(Worker $w, array $calls): array {
    $replies = [];
    foreach ($calls as $i => $call) {
        $w->send(envelope($call, $i + 1) . "\n");
        $line = $w->line();
        $r = reply($line);
        if ($r === null) {
            $replies[] = null;
            return [$replies, 'envelope ' . ($i + 1) . ': ' . ($line === null ? 'no reply line' : 'not a reply: ' . json_encode(trim($line)))];
        }
        $replies[] = $r;
    }
    return [$replies, ''];
}

function program(): string {
    static $dir = null;
    if ($dir === null) {
        $dir = workdir();
        register_shutdown_function('remove_tree', $dir);
    }
    return $dir . '/program.php';
}

echo "PHP SDK — \\Drift\\run serves many calls from one process\n";

// ── what PHP does with a scratch directory ──────────────────────────────────
//
// The loop's scratch rule rests on this, so it is recorded here as what PHP
// does and asserted as the answer the rule was built on: if a later PHP reads
// TMPDIR again on every call, the second block is no longer the case and the
// rule in \Drift\run can be simpler.
echo "\nscratch directory on the second call, recorded\n";
{
    $dir = workdir();
    $probe = $dir . '/probe.php';
    file_put_contents($probe, <<<'PHP'
<?php
$base = $argv[1];
mkdir("$base/one");
mkdir("$base/two");
function seen(string $call, string $base): array {
    $temp = sys_get_temp_dir();
    $name = @tempnam($temp, 'x');
    $handle = @tmpfile();
    return [
        'call' => $call,
        'TMPDIR' => getenv('TMPDIR'),
        'sys_get_temp_dir' => $temp,
        'tempnam' => $name === false ? false : dirname($name),
        'tmpfile' => $handle === false ? false : dirname(stream_get_meta_data($handle)['uri']),
    ];
}
$out = [];
putenv("TMPDIR=$base/one");
$out[] = seen('first', $base);
foreach (glob("$base/one/*") as $f) unlink($f);
rmdir("$base/one");
putenv("TMPDIR=$base/two");
$out[] = seen('second, after the first directory was removed', $base);
echo json_encode($out);
PHP);
    $json = shell_exec(escapeshellarg(PHP_BINARY) . ' -n ' . escapeshellarg($probe) . ' ' . escapeshellarg($dir) . ' 2>/dev/null');
    $seen = json_decode((string) $json, true);
    if (!is_array($seen) || count($seen) !== 2) {
        check('the probe ran', false, var_export($json, true));
    } else {
        foreach ($seen as $s) {
            observe(sprintf('%s: TMPDIR=%s sys_get_temp_dir=%s tempnam in %s tmpfile in %s',
                $s['call'], $s['TMPDIR'], $s['sys_get_temp_dir'], var_export($s['tempnam'], true), var_export($s['tmpfile'], true)));
        }
        check('sys_get_temp_dir() keeps the first call\'s directory, though TMPDIR now names another',
            $seen[1]['sys_get_temp_dir'] === $dir . '/one' && $seen[1]['TMPDIR'] === $dir . '/two',
            json_encode($seen[1]));
        check('tempnam() and tmpfile() fail once that directory is removed',
            $seen[1]['tempnam'] === false && $seen[1]['tmpfile'] === false, json_encode($seen[1]));
    }
    remove_tree($dir);
}

// ── three calls, one process, handler output away from the reply ────────────
echo "\nthree envelopes, one process\n";
{
    $w = new Worker(program());
    [$replies, $problem] = converse($w, [
        ['tmpdir' => '/scratch/loop-1', 'body' => ['echo' => 'loop-test-echo', 'warn' => true]],
        ['tmpdir' => '/scratch/loop-2', 'body' => ['print' => 'loop-test-print', 'notice' => true, 'error_log' => 'loop-test-error-log']],
        ['tmpdir' => '/scratch/loop-3'],
    ]);
    check('three intact reply lines', $problem === '' && count($replies) === 3, $problem);
    $pids = array_map(fn($r) => $r['payload']['pid'] ?? null, array_filter($replies));
    check('from the one process the test started', count($pids) === 3 && count(array_unique($pids)) === 1 && $pids[0] === $w->pid,
        json_encode($pids) . ' vs ' . $w->pid);

    foreach (['loop-test-echo' => 'echo', 'loop-test-print' => 'print', 'Undefined variable' => 'a warning', 'loop-test-notice' => 'a notice', 'loop-test-error-log' => 'error_log'] as $text => $what) {
        check("$what reaches stderr", str_contains($w->stderr(), $text), $w->stderr());
    }

    $w->closeStdin();
    [$rest, $closed] = $w->rest();
    check('and nothing else is written to the response stream', $closed && $rest === '', json_encode($rest));
    check('end of input ends the process with status 0', $w->exit() === 0, var_export($w->exit(0), true));
    $w->stop();
}

// ── a handler that writes the STDOUT constant corrupts its own call ─────────
//
// The SDK cannot stop it: STDOUT is the response stream. What matters is that
// the damage is detected rather than silent: the slice reads the stray line as
// the reply, cannot parse it, fails that call and retires the worker.
echo "\na handler that writes the STDOUT constant itself\n";
{
    $w = new Worker(program());
    $w->send(envelope(['body' => ['stdout' => "loop-test-stray\n"]], 1) . "\n");
    $first = $w->line();
    check('the first line on the response stream is the stray one, which is not a reply', $first === "loop-test-stray\n" && reply($first) === null, json_encode($first));
    $w->stop();
}

// ── a handler that raises ends the process ──────────────────────────────────
echo "\na handler that raises\n";
{
    $w = new Worker(program());
    [$replies, $problem] = converse($w, [[], []]);
    $w->send(envelope(['body' => ['fail' => true]], 3) . "\n");
    [$written, $closed] = $w->rest(1.0);
    $status = $w->exit();
    check('two calls answered first', $problem === '' && count($replies) === 2, $problem);
    check('then no output at all', $closed && $written === '', json_encode($written));
    check('and a non-zero status', $status !== null && $status !== 0, var_export($status, true));
    check('with the failure on stderr', str_contains($w->stderr(), 'loop-test-failure'), $w->stderr());
    $w->stop();
}

// ── TMPDIR, HOME and secrets agree in getenv, $_ENV and $_SERVER ────────────
echo "\nTMPDIR, HOME and secrets, three ways\n";
{
    $w = new Worker(program());
    $calls = [
        ['tmpdir' => '/scratch/one', 'secrets' => ['API_KEY' => 'key-one', 'webhook' => 'token'], 'secrets_previous' => ['API_KEY' => 'old']],
        ['tmpdir' => '/scratch/two', 'secrets' => ['API_KEY' => 'key-two']],
        ['tmpdir' => '/scratch/three'],
    ];
    [$replies, $problem] = converse($w, $calls);
    check('three replies', $problem === '' && count($replies) === 3, $problem);
    if ($problem === '') {
        $want = [
            ['/scratch/one', ['DRIFT_SECRET_API_KEY' => 'key-one', 'DRIFT_SECRET_API_KEY_PREVIOUS' => 'old', 'DRIFT_SECRET_WEBHOOK' => 'token']],
            ['/scratch/two', ['DRIFT_SECRET_API_KEY' => 'key-two']],
            ['/scratch/three', []],
        ];
        foreach ($want as $i => [$dir, $secrets]) {
            $p = $replies[$i]['payload'];
            $n = $i + 1;
            foreach (['getenv', 'env', 'server'] as $source) {
                check("call $n: $source reads TMPDIR and HOME as the envelope's directory",
                    $p['tmpdir'][$source] === $dir && $p['home'][$source] === $dir,
                    json_encode([$p['tmpdir'][$source], $p['home'][$source]]));
                check("call $n: $source holds exactly the carried secrets",
                    json_encode($p['secrets'][$source], JSON_FORCE_OBJECT) === json_encode($secrets, JSON_FORCE_OBJECT),
                    json_encode($p['secrets'][$source]));
            }
        }
    }
    $w->stop();
}

// ── every reply reports the process's peak resident size ────────────────────
echo "\nrss_bytes\n";
if (is_readable('/proc/self/status')) {
    $w = new Worker(program());
    [$replies, $problem] = converse($w, [[], []]);
    $rss = array_map(fn($r) => $r['rss_bytes'] ?? null, $replies);
    check('a positive integer in every reply', $problem === '' && count($rss) === 2 && array_reduce($rss, fn($ok, $v) => $ok && is_int($v) && $v > 0, true),
        json_encode($rss) . $problem);
    $w->stop();
} else {
    observe('no /proc/self/status here: the reply carries no rss_bytes and nothing is asserted');
}

// ── the one-shot spawn ──────────────────────────────────────────────────────
echo "\none envelope, no trailing newline, then end of input\n";
{
    $w = new Worker(program());
    $w->send(envelope(['tmpdir' => '/scratch/once'], 1));
    $w->closeStdin();
    [$data, $closed] = $w->rest();
    $lines = array_values(array_filter(explode("\n", $data), fn($l) => trim($l) !== ''));
    check('one reply', $closed && count($lines) === 1 && reply($lines[0]) !== null, json_encode($data));
    check('and status 0', $w->exit() === 0, var_export($w->exit(0), true));
    $w->stop();
}

// ── the scratch directory a worker serves from ──────────────────────────────
//
// A file one call writes into the directory sys_get_temp_dir() names is not
// readable by the next call, tempnam() and tmpfile() keep working after the
// slice has removed an envelope's own directory, and the worker's directory is
// gone when it ends.
echo "\nthe scratch directory\n";
{
    $a = sys_get_temp_dir() . '/drift-loop-test-a-' . getmypid();
    $b = sys_get_temp_dir() . '/drift-loop-test-b-' . getmypid();
    mkdir($a);
    mkdir($b);
    $w = new Worker(program());
    $w->send(envelope(['tmpdir' => $a, 'body' => ['write' => 'carried.txt', 'scratch' => true]], 1) . "\n");
    $first = reply($w->line());
    remove_tree($a);
    $w->send(envelope(['tmpdir' => $b, 'body' => ['look' => 'carried.txt', 'scratch' => true]], 2) . "\n");
    $second = reply($w->line());
    check('two replies', $first !== null && $second !== null);
    if ($first !== null && $second !== null) {
        $own = $first['payload']['temp'];
        observe("the worker's own scratch directory: $own");
        check('the directory is the same on both calls', $second['payload']['temp'] === $own);
        check('and is not the envelope\'s, which the slice removes', $own !== $a && $own !== $b);
        check('a file written in one call is not readable by the next', $second['payload']['seen'] === false,
            json_encode($second['payload']['seen']));
        check('tempnam() and tmpfile() work on both calls, in that directory',
            $first['payload']['tempnam'] === $own && $first['payload']['tmpfile'] === $own
            && $second['payload']['tempnam'] === $own && $second['payload']['tmpfile'] === $own,
            json_encode([$first['payload']['tempnam'], $first['payload']['tmpfile'], $second['payload']['tempnam'], $second['payload']['tmpfile']]));
        $w->closeStdin();
        $w->exit();
        check('the directory is gone when the worker ends', !file_exists($own), $own);
    }
    $w->stop();
    remove_tree($b);
}

// ── the loop is the deployed path only ──────────────────────────────────────
echo "\nwithout DRIFT_RUNTIME\n";
{
    $probe = stream_socket_server('tcp://127.0.0.1:0', $errno, $errstr);
    $port = (int) substr(strrchr((string) stream_socket_get_name($probe, false), ':'), 1);
    fclose($probe);
    $w = new Worker(program(), ['PORT' => (string) $port]);
    $answer = '';
    for ($i = 0; $i < 100; $i++) {
        $s = @stream_socket_client("tcp://127.0.0.1:$port", $errno, $errstr, 0.2);
        if ($s) {
            fwrite($s, "GET /local HTTP/1.1\r\nHost: x\r\n\r\n");
            stream_set_timeout($s, 2);
            $answer = (string) stream_get_contents($s);
            fclose($s);
            break;
        }
        usleep(50000);
    }
    check('the local server answers a request', str_contains($answer, '"status":200') && str_contains($answer, '"pid"'), $answer);
    $w->stop();
}

// ── the constant the CLI looks for ──────────────────────────────────────────
echo "\nLOOP_PROTOCOL\n";
check('\\Drift\\LOOP_PROTOCOL is 1', defined('\\Drift\\LOOP_PROTOCOL') && \constant('\\Drift\\LOOP_PROTOCOL') === 1);

echo $failures === 0
    ? "\n✅ every case passed\n"
    : "\n❌ $failures assertion(s) failed\n";
exit($failures === 0 ? 0 : 1);
