# The Ruby program the loop cases run (loop_check.py).
#
# It serves one handler through Drift.run, the entry point the generated Ruby
# wrapper calls, so what the cases assert is what a slice's warm worker gets. It
# re-implements no loop.
#
# The handler reports what a function can see of its call: its pid, TMPDIR and
# HOME, and every DRIFT_SECRET_ variable. Two requests change what it does: a
# body with "print" writes that text to stdout, as a function's own puts does,
# and a body with "fail" raises. The SDK's own Ruby test (sdk/ruby/loop_test.rb)
# also uses "fd1", which writes that text to file descriptor 1 directly, as a
# native extension does, and reads Dir.tmpdir and Dir.home from the payload.

require 'json'
require 'tmpdir'

require_relative '../ruby/drift'

def handle(req)
  ask = req['body'].is_a?(Hash) ? req['body'] : {}
  puts ask['print'] unless ask['print'].nil?
  IO.for_fd(1, autoclose: false).syswrite("#{ask['fd1']}\n") unless ask['fd1'].nil?
  raise 'loop conformance: the handler was asked to fail' if ask['fail']

  {
    'status' => 200,
    'message' => 'ok',
    'payload' => {
      'pid' => Process.pid,
      'tmpdir' => ENV['TMPDIR'],
      'home' => ENV['HOME'],
      'dir_tmpdir' => Dir.tmpdir,
      'dir_home' => Dir.home,
      'secrets' => ENV.to_h.select { |name, _| name.start_with?('DRIFT_SECRET_') },
    },
  }
end

Drift.run(method(:handle))
