# The Ruby program the start check cases run (probe_check.py).
#
# It hands a handler that must not run to the SDK's own entry points, Drift.run
# for the entry "run" and Drift.run_sse and Drift.run_ws for the streaming ones.
# Its two arguments are the entry's name and the marker the handler writes to
# stderr, before it exits 97, if it is ever reached.

require_relative '../ruby/drift'

entry, marker = ARGV
handler = lambda do |*|
  $stderr.write("#{marker}\n")
  $stderr.flush
  exit!(97)
end

case entry
when 'run' then Drift.run(handler)
when 'run_sse' then Drift.run_sse(&handler)
when 'run_ws' then Drift.run_ws(&handler)
else
  warn 'usage: probe_ruby.rb <run|run_sse|run_ws> <marker>'
  exit 2
end
