# loop_test.rb — Drift.run under DRIFT_RUNTIME: one process serving many calls.
#
# Run: ruby sdk/ruby/loop_test.rb
#
# The program is sdk/conformance/loop_ruby.rb, started as a slice starts a warm
# worker: DRIFT_RUNTIME set, nothing else in its environment, envelopes written
# one per line to its stdin, one reply line read back for each. The same
# conversation, for every SDK, is held by sdk/conformance; these tests are the
# SDK's own, so this file fails here before the conformance job gets to it.
# Stdlib only.

require 'json'
require 'open3'
require 'rbconfig'
require 'timeout'
require 'tmpdir'

$failures = 0

def check(what, ok, detail = '')
  if ok
    puts "  ✓ #{what}"
    return
  end
  puts "  ✗ #{what}#{detail.to_s.empty? ? '' : ": #{detail}"}"
  $failures += 1
end

# How long a worker has to answer an envelope or to end after its input does.
WAIT = 5

PROGRAM = File.expand_path('../conformance/loop_ruby.rb', __dir__)

# A worker: its pipes, the lines it has written to stdout and everything it has
# written to stderr.
class Worker
  attr_reader :pid

  def initialize
    @stdin, stdout, stderr, @wait = Open3.popen3(
      { 'DRIFT_RUNTIME' => '1' }, RbConfig.ruby, PROGRAM, unsetenv_others: true
    )
    @pid = @wait.pid
    @lines = Queue.new
    @stderr = +''
    @readers = [
      Thread.new { stdout.each_line { |l| @lines << l.chomp } },
      Thread.new { stderr.each_line { |l| @stderr << l } },
    ]
  end

  # Write one envelope and its newline.
  def send_envelope(envelope)
    @stdin.write("#{JSON.generate(envelope)}\n")
    @stdin.flush
  end

  # Write text exactly as given, with no newline added.
  def write_raw(text)
    @stdin.write(text)
    @stdin.flush
  end

  # The next line the worker wrote to stdout, or nil when none comes in time.
  def reply_line(within = WAIT)
    Timeout.timeout(within) { @lines.pop }
  rescue Timeout::Error
    nil
  end

  # The next reply, parsed; nil when no line came or it was not JSON, because
  # stdout carries replies and nothing else.
  def reply
    line = reply_line
    line && JSON.parse(line)
  rescue JSON::ParserError
    nil
  end

  # One envelope in, its reply out.
  def call(envelope)
    send_envelope(envelope)
    reply
  end

  # Close stdin, as the slice does to retire a worker.
  def close_stdin
    @stdin.close unless @stdin.closed?
  end

  # The exit status once the process has ended; nil if it outlives WAIT.
  def exit_status
    Timeout.timeout(WAIT) { @wait.value }
  rescue Timeout::Error
    nil
  end

  def stderr
    @readers.each { |t| t.join(1) }
    @stderr
  end

  def stop
    close_stdin
    Process.kill('KILL', @pid) if @wait.alive?
  rescue Errno::ESRCH
    nil
  ensure
    @wait.join
  end
end

def with_worker
  worker = Worker.new
  yield worker
ensure
  worker&.stop
end

def payload(reply)
  reply.is_a?(Hash) ? reply['payload'] : nil
end

puts 'three envelopes are answered by one process'
with_worker do |worker|
  pids = (0...3).map do |i|
    reply = worker.call({ 'path' => '/', 'headers' => {}, 'body' => { 'n' => i } })
    check("call #{i + 1} answers 200", reply && reply['status'] == 200, reply.inspect)
    payload(reply)&.fetch('pid', nil)
  end
  check('every call reports a pid', pids.none?(&:nil?), pids.inspect)
  check('calls 1, 2 and 3 are served by one process', pids.none?(&:nil?) && pids.uniq.length == 1, pids.inspect)
  check('the pid is the process the test started', pids.first == worker.pid, pids.inspect)
end

# A trigger's spawn: one envelope, no trailing newline, then end of input. It is
# answered once and the process exits 0. This holds before and after the loop,
# which is the point of it.
puts 'one envelope without a newline is answered once'
with_worker do |worker|
  worker.write_raw('{"path":"/","headers":{},"body":{}}')
  worker.close_stdin
  reply = worker.reply
  check('the envelope is answered 200', reply && reply['status'] == 200, reply.inspect)
  status = worker.exit_status
  check('the process exits 0', status && status.exitstatus == 0, status.inspect)
  extra = worker.reply_line(0.3)
  check('no second line follows the one reply', extra.nil?, extra.inspect)
end

puts 'a printed line reaches stderr and the reply stays one line'
with_worker do |worker|
  first = worker.call({ 'path' => '/', 'body' => { 'print' => 'loop-test-printed-line' } })
  check('the call that printed is answered 200', first && first['status'] == 200, first.inspect)
  second = worker.call({ 'path' => '/', 'body' => {} })
  check('the call after the print is answered 200', second && second['status'] == 200, second.inspect)
  worker.close_stdin
  status = worker.exit_status
  check('the process exits 0', status && status.exitstatus == 0, status.inspect)
  check('the printed text is on stderr', worker.stderr.include?('loop-test-printed-line'), worker.stderr.inspect)
end

puts 'a write to file descriptor 1 reaches stderr and the reply stays one line'
with_worker do |worker|
  first = worker.call({ 'path' => '/', 'body' => { 'fd1' => 'loop-test-fd1-line' } })
  check('the call that wrote fd 1 is answered 200', first && first['status'] == 200, first.inspect)
  second = worker.call({ 'path' => '/', 'body' => {} })
  check('the call after the write is answered 200', second && second['status'] == 200, second.inspect)
  worker.close_stdin
  worker.exit_status
  check('the text is on stderr', worker.stderr.include?('loop-test-fd1-line'), worker.stderr.inspect)
end

puts 'end of input ends the process with status 0'
with_worker do |worker|
  3.times { |i| worker.call({ 'path' => '/', 'body' => { 'n' => i } }) }
  worker.close_stdin
  status = worker.exit_status
  check('the process exits 0 after three calls', status && status.exitstatus == 0, status.inspect)
end

puts 'the scratch directory is the envelope\'s on every call'
Dir.mktmpdir('drift-loop-test') do |root|
  with_worker do |worker|
    %w[one two three].each do |name|
      dir = File.join(root, name)
      # 0700 as a slice makes a call's scratch directory: Dir.tmpdir passes over a
      # world-writable directory without the sticky bit, which a umask of 0 would make.
      Dir.mkdir(dir, 0o700)
      seen = payload(worker.call({ 'path' => '/', 'tmpdir' => dir }))
      check("TMPDIR is #{name}", seen && seen['tmpdir'] == dir, seen.inspect)
      check("HOME is #{name}", seen && seen['home'] == dir, seen.inspect)
      check("Dir.tmpdir is #{name}", seen && seen['dir_tmpdir'] == dir, seen.inspect)
      check("Dir.home is #{name}", seen && seen['dir_home'] == dir, seen.inspect)
    end
  end
end

puts 'a relative scratch directory is ignored'
with_worker do |worker|
  seen = payload(worker.call({ 'path' => '/', 'tmpdir' => 'relative/dir' }))
  check('TMPDIR stays unset', seen && seen['tmpdir'].nil?, seen.inspect)
end

puts 'a secret the envelope no longer carries is gone'
with_worker do |worker|
  secrets = ->(reply) { payload(reply)&.fetch('secrets', nil) }

  one = worker.call({ 'secrets' => { 'API_KEY' => 'key-one', 'WEBHOOK_TOKEN' => 'token' } })
  check('both secrets are set', secrets.(one) == { 'DRIFT_SECRET_API_KEY' => 'key-one', 'DRIFT_SECRET_WEBHOOK_TOKEN' => 'token' }, secrets.(one).inspect)

  two = worker.call({ 'secrets' => { 'API_KEY' => 'key-two' } })
  check('the changed one is replaced and the dropped one is gone', secrets.(two) == { 'DRIFT_SECRET_API_KEY' => 'key-two' }, secrets.(two).inspect)

  three = worker.call({})
  check('an envelope with none leaves none', secrets.(three) == {}, secrets.(three).inspect)
end

puts 'a previous secret follows the same rule'
with_worker do |worker|
  secrets = ->(reply) { payload(reply)&.fetch('secrets', nil) }

  one = worker.call({ 'secrets' => { 'API_KEY' => 'new' }, 'secrets_previous' => { 'API_KEY' => 'old' } })
  check('both values are set', secrets.(one) == { 'DRIFT_SECRET_API_KEY' => 'new', 'DRIFT_SECRET_API_KEY_PREVIOUS' => 'old' }, secrets.(one).inspect)

  two = worker.call({ 'secrets' => { 'API_KEY' => 'new' } })
  check('the previous value is gone when not carried', secrets.(two) == { 'DRIFT_SECRET_API_KEY' => 'new' }, secrets.(two).inspect)
end

if File.exist?('/proc/self/status')
  puts 'every reply carries the peak resident size'
  with_worker do |worker|
    3.times do |i|
      reply = worker.call({ 'path' => '/', 'body' => { 'n' => i } })
      rss = reply && reply['rss_bytes']
      check("call #{i + 1} carries a positive rss_bytes", rss.is_a?(Integer) && rss.positive?, reply.inspect)
    end
  end
end

puts 'a handler that raises ends the process'
with_worker do |worker|
  ok = worker.call({ 'path' => '/', 'body' => {} })
  check('a call before the failure is answered', ok && ok['status'] == 200, ok.inspect)
  worker.send_envelope({ 'path' => '/', 'body' => { 'fail' => true } })
  status = worker.exit_status
  check('the process exits non-zero', status && !status.exitstatus.zero?, status.inspect)
  extra = worker.reply_line(0.3)
  check('no reply follows the failure', extra.nil?, extra.inspect)
end

puts 'the SDK names its loop for the CLI'
require_relative 'drift'
check('Drift::LOOP_PROTOCOL is 1', defined?(Drift::LOOP_PROTOCOL) && Drift::LOOP_PROTOCOL == 1,
      defined?(Drift::LOOP_PROTOCOL) ? Drift::LOOP_PROTOCOL.inspect : 'not defined')

puts($failures.zero? ? "\n✅ every case passed" : "\n❌ #{$failures} assertion(s) failed")
exit($failures.zero? ? 0 : 1)
