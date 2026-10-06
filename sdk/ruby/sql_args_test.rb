# sql_args_test.rb — a SQL statement always sends `args` as a JSON array.
#
# Run: ruby sdk/ruby/sql_args_test.rb
#
# A slice reads `args` as a list and refuses `"args":null` as invalid JSON, so
# a statement that sends null fails before it reaches SQLite. The conformance
# harness runs against the local store and never serialises a request, so this
# stands up a real TCP server on loopback, points BACKBONE_URL at it, and reads
# the bodies the SDK actually sends. Stdlib only: no webrick.

require 'json'
require 'socket'

$failures = 0

def check(what, ok, detail = '')
  if ok
    puts "  ✓ #{what}"
    return
  end
  puts "  ✗ #{what}#{detail.empty? ? '' : ": #{detail}"}"
  $failures += 1
end

REPLIES = {
  '/sql/query' => '{"columns":["n"],"rows":[[1]]}',
  '/sql/execute' => '{"rows_affected":0,"last_insert_id":0}',
  '/sql/begin' => '{"tx":"t1"}',
}.freeze

bodies = Queue.new
server = TCPServer.new('127.0.0.1', 0)
Thread.new do
  loop do
    # Closing the server at the end of the run ends this loop.
    sock = begin
      server.accept
    rescue IOError
      break
    end
    begin
      request_line = sock.gets.to_s
      path = request_line.split(' ')[1].to_s
      length = 0
      while (line = sock.gets) && line.strip != ''
        k, v = line.split(':', 2)
        length = v.to_i if k.to_s.downcase == 'content-length'
      end
      raw = length.positive? ? sock.read(length) : ''
      bodies << [path, raw]
      reply = REPLIES[path] || '{}'
      sock.write("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n" \
                 "Content-Length: #{reply.bytesize}\r\nConnection: close\r\n\r\n#{reply}")
    ensure
      sock.close
    end
  end
end

ENV['BACKBONE_URL'] = "http://127.0.0.1:#{server.addr[1]}"
require_relative 'drift'

# The raw `args` text of the next body sent, as the slice parses it.
def args_of(bodies, want_path)
  path, raw = bodies.pop
  return "(request went to #{path})" unless path == want_path
  JSON.generate(JSON.parse(raw)['args'])
end

puts 'Ruby SDK — a SQL statement always sends an args array'

db = Drift::Backbone.sql('main')

db.query('SELECT 1')
got = args_of(bodies, '/sql/query')
check('query with no args sends []', got == '[]', got)
db.query('SELECT 1', nil)
got = args_of(bodies, '/sql/query')
check('query with args nil sends []', got == '[]', got)
db.execute('DELETE FROM t')
got = args_of(bodies, '/sql/execute')
check('execute with no args sends []', got == '[]', got)
db.execute('DELETE FROM t', nil)
got = args_of(bodies, '/sql/execute')
check('execute with args nil sends []', got == '[]', got)

tx = db.begin
bodies.pop
tx.query('SELECT 1', nil)
got = args_of(bodies, '/sql/query')
check('tx.query with args nil sends []', got == '[]', got)
tx.execute('DELETE FROM t', nil)
path, raw = bodies.pop
sent = JSON.parse(raw)
check('tx.execute with args nil sends []', path == '/sql/execute' && sent['args'] == [], raw)
check('a transaction keeps its token', sent['tx'] == 't1', raw)

# THE CONTROL: arguments that are passed arrive unchanged and in order.
db.query('SELECT ?, ?', ['a', 2])
got = args_of(bodies, '/sql/query')
check('passed args arrive in order', got == '["a",2]', got)

server.close
puts($failures.zero? ? "\n✅ every case passed" : "\n❌ #{$failures} assertion(s) failed")
exit($failures.zero? ? 0 : 1)
