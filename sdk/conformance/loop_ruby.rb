# The Ruby program the loop cases run (loop_check.py).
#
# It serves one handler through Drift.run, the entry point the generated Ruby
# wrapper calls, so what the cases assert is what a slice's warm worker gets. It
# re-implements no loop.
#
# The handler reports what a function can see of its call: its pid, TMPDIR and
# HOME, and every DRIFT_SECRET_ variable. Two requests change what it does: a
# body with "print" writes that text to stdout, as a function's own puts does,
# and a body with "fail" raises.

require 'json'

require_relative '../ruby/drift'

def handle(req)
  ask = req['body'].is_a?(Hash) ? req['body'] : {}
  puts ask['print'] unless ask['print'].nil?
  raise 'loop conformance: the handler was asked to fail' if ask['fail']

  {
    'status' => 200,
    'message' => 'ok',
    'payload' => {
      'pid' => Process.pid,
      'tmpdir' => ENV['TMPDIR'],
      'home' => ENV['HOME'],
      'secrets' => ENV.to_h.select { |name, _| name.start_with?('DRIFT_SECRET_') },
    },
  }
end

Drift.run(method(:handle))
