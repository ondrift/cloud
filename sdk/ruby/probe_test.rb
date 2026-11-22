# probe_test.rb: a process started to be checked says hello and exits, and never
# reaches the handler.
#
# Run: ruby sdk/ruby/probe_test.rb
#
# Before a deploy replaces the function that serves, the slice starts the new
# version once: DRIFT_INVOKE=probe1, stdin closed. It wants one line on stdout,
# {"hello":{"proto":"drift-probe","v":1}}, and exit status 0, with the handler
# never called, because a handler writes to databases and sends mail. The
# platform owns that contract; this holds the SDK's side of it.
#
# Answering means leaving the process, so each entry point is started as a
# process of its own. The handler in that process fails the test if it is
# called: it writes HANDLER_CALLED to stderr and exits 97. Each entry also runs
# without the check, with a request on stdin, where the handler IS reached;
# without that the absence of the marker could mean the guard cannot fire.

require 'open3'
require 'rbconfig'

$failures = 0

def check(what, ok, detail = '')
  if ok
    puts "  ✓ #{what}"
    return
  end
  puts "  ✗ #{what}#{detail.empty? ? '' : ": #{detail}"}"
  $failures += 1
end

DRIFT = File.expand_path('drift.rb', __dir__)

# The line the platform's start check reads, as the SDK repeats it.
HELLO = %({"hello":{"proto":"drift-probe","v":1}}\n).freeze
CALLED = 'HANDLER_CALLED'

# The slice allows a check ten seconds from spawn.
WITHIN = 10

# Every entry point the SDK has that reads a request.
ENTRIES = %w[run run_sse run_ws].freeze

# What an entry reads when it is not being checked: a request, on one line.
REQUEST = %({"path":"/","headers":{},"query":"","body":null}\n).freeze

PROGRAM = <<~RUBY
  require ARGV[1]
  handler = lambda do |*|
    $stderr.write("#{CALLED}\\n")
    $stderr.flush
    exit!(97)
  end
  case ARGV[0]
  when 'run' then Drift.run(handler)
  when 'run_sse' then Drift.run_sse(&handler)
  when 'run_ws' then Drift.run_ws(&handler)
  end
RUBY

Finished = Struct.new(:stdout, :stderr, :status)

# The entry point as its own process, as the slice starts a function. A request
# of nil is a closed stdin: nothing is written and the pipe is closed at once, so
# the process reads end of input. Otherwise the request is written first. Returns
# the finished process, or nil when it was still running after WITHIN seconds.
def start(entry, invoke, request)
  env = { 'DRIFT_RUNTIME' => '1' }
  env['DRIFT_INVOKE'] = invoke unless invoke.nil?
  out = err = ''
  status = nil
  timed_out = false
  Open3.popen3(env, RbConfig.ruby, '-e', PROGRAM, entry, DRIFT, unsetenv_others: true) do |stdin, stdout, stderr, wait|
    stdin.write(request) unless request.nil?
    stdin.close
    killer = Thread.new do
      sleep WITHIN
      timed_out = true
      begin
        Process.kill('KILL', wait.pid)
      rescue SystemCallError
        nil
      end
    end
    out = stdout.read
    err = stderr.read
    status = wait.value
    killer.kill
  end
  timed_out ? nil : Finished.new(out, err, status)
end

# The line of what a process wrote to stderr that says why it failed: the last
# one that is not a backtrace frame.
def tail(text)
  last = text.strip.lines.map(&:strip).reject { |l| l.start_with?('from ') }.last.to_s
  last.length <= 160 ? last : "...#{last[-160..]}"
end

def describe(done)
  "stdout #{done.stdout.inspect}, status #{done.status.exitstatus.inspect}, stderr #{tail(done.stderr).inspect}"
end

require_relative 'drift'

puts 'Ruby SDK: the start check'

version = defined?(Drift::PROBE_PROTOCOL) ? Drift::PROBE_PROTOCOL : nil
check('Drift::PROBE_PROTOCOL is 1', version == 1, version.inspect)

ENTRIES.each do |entry|
  done = start(entry, 'probe1', nil)
  if done.nil?
    check("#{entry} answers within #{WITHIN}s", false, 'it was still running')
    next
  end
  check("#{entry} writes the hello line and nothing else", done.stdout == HELLO, describe(done))
  check("#{entry} exits 0", done.status.exited? && done.status.exitstatus.zero?, describe(done))
  check("#{entry} does not reach the handler", !done.stderr.include?(CALLED), describe(done))
end

# THE CONTROLS. Without the check an entry reads its request and calls the
# handler, so the marker above can appear, and an SDK that answered every start
# as a check would show here.
[[nil, 'without the check'], ['probe2', 'under another version of the check']].each do |invoke, what|
  ENTRIES.each do |entry|
    done = start(entry, invoke, REQUEST)
    if done.nil?
      check("#{entry} #{what} reaches the handler", false, "it was still running after #{WITHIN}s")
      next
    end
    check("#{entry} #{what} reaches the handler and says no hello",
          done.stderr.include?(CALLED) && done.status.exitstatus == 97 && !done.stdout.include?('drift-probe'),
          describe(done))
  end
end

puts $failures.zero? ? "\n✅ every case passed" : "\n❌ #{$failures} assertion(s) failed"
exit($failures.zero? ? 0 : 1)
