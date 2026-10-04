# Drives the Ruby SDK through the shared conformance cases and prints what it
# got, as JSON, for check.py to compare against expected.json.
#
# It asserts NOTHING itself. The assertions are in expected.json, once, for all
# six languages — six independently-worded suites can each pass while asserting
# subtly different things, and that is how five SDKs came to disagree with Go
# about local blobs without anybody noticing.

require 'json'

# No BACKBONE_URL: every case runs against the in-memory local store, which is
# the loop a developer meets first and the one where the six drifted. Set BEFORE
# the require, because the SDK caches the value on first read.
ENV['BACKBONE_URL'] = ''

require_relative '../ruby/drift'

OUT = {}

def emit
  puts JSON.pretty_generate(OUT)
end

# Report the first thing that went wrong and stop. A driver that carried on would
# report later cases against a store in a state nobody intended.
def fail_with(message)
  OUT['error'] = message
  emit
  exit 0 # check.py turns the reported error into the failure
end

blob = Drift::Backbone::Blob

# -- Blob --------------------------------------------------------------------
begin
  blob.put('uploads/greeting.txt', 'hello, slice', content_type: 'text/plain')
rescue StandardError => e
  fail_with("Blob.put with a bucket: #{e.message}")
end
begin
  OUT['blob_with_bucket'] = blob.get('uploads/greeting.txt')
rescue StandardError => e
  fail_with("Blob.get with a bucket: #{e.message}")
end

begin
  blob.put('greeting.txt', 'bare')
rescue StandardError => e
  fail_with("Blob.put without a bucket: #{e.message}")
end
begin
  OUT['blob_without_bucket'] = blob.get('greeting.txt')
rescue StandardError => e
  fail_with("Blob.get without a bucket: #{e.message}")
end

# A key never written must FAIL, and "error" is what a failure reports here. An
# SDK that returns empty-with-no-error reports null instead and fails the case —
# which is the whole point, because an implementation returning empty for
# EVERYTHING would otherwise pass the two cases above by accident.
begin
  got = blob.get('uploads/never-written.txt')
  OUT['blob_absent'] = got.nil? || got.empty? ? nil : got
rescue StandardError
  OUT['blob_absent'] = 'error'
end

# -- Ordered NoSQL reads -----------------------------------------------------
small = Drift::Backbone::Nosql.collection('conformance_small')
(1..12).each do |i|
  small.insert({ 'n' => i })
rescue StandardError => e
  fail_with("Insert #{i}: #{e.message}")
end

begin
  OUT['list_in_order'] = small.list_in_order(limit: 100).map { |d| d['n'] }
rescue StandardError => e
  fail_with("list_in_order: #{e.message}")
end
begin
  OUT['list_default_order'] = small.list.map { |d| d['_key'] }
rescue StandardError => e
  fail_with("list: #{e.message}")
end

# Past the 1000-row page cap, which is where a cursor that advances in KEY order
# rather than in the ordered sequence silently truncates.
big = Drift::Backbone::Nosql.collection('conformance_big')
(1..1200).each do |i|
  big.insert({ 'n' => i })
rescue StandardError => e
  fail_with("Insert big #{i}: #{e.message}")
end
begin
  ns = big.list_all_in_order.map { |d| d['n'] }
  OUT['list_all_in_order_count'] = ns.length
  unless ns.empty?
    OUT['list_all_in_order_first'] = ns.first
    OUT['list_all_in_order_last'] = ns.last
  end
  OUT['list_all_in_order_boundary'] = ns[998..1001] if ns.length >= 1002
rescue StandardError => e
  fail_with("list_all_in_order: #{e.message}")
end

# -- Filtered NoSQL reads ----------------------------------------------------
filtered = Drift::Backbone::Nosql.collection('conformance_filter')
wide = { '_id' => 'wide' }
9.times { |i| wide["f#{i}"] = 'v' }
[
  { '_id' => 'x1', 'a' => '1', 'b' => '1' },
  { '_id' => 'x2', 'a' => '1', 'b' => '2' },
  { '_id' => 'x3', 'a' => '2', 'b' => '1' },
  { '_id' => 'x4', 'a' => '3', 'b' => '2' },
  wide,
].each do |doc|
  filtered.insert(doc)
rescue StandardError => e
  fail_with("Insert #{doc['_id']}: #{e.message}")
end
begin
  OUT['list_filter_two_fields'] = filtered.list({ 'a' => '1', 'b' => '2' }).map { |d| d['_id'] }.sort
rescue StandardError => e
  fail_with("list with two entries: #{e.message}")
end
begin
  OUT['list_filter_one_field'] = filtered.list({ 'a' => '1' }).map { |d| d['_id'] }.sort
rescue StandardError => e
  fail_with("list with one entry: #{e.message}")
end
begin
  nine = (0...9).to_h { |i| ["f#{i}", 'v'] }
  OUT['list_filter_over_cap'] = filtered.list(nine).map { |d| d['_id'] }.sort
rescue StandardError
  OUT['list_filter_over_cap'] = 'error'
end

# -- Edge links --------------------------------------------------------------
# A loopback peer that echoes the path it was asked for and the identity header
# it received, null when the header is absent.
peer = TCPServer.new('127.0.0.1', 0)
Thread.new do
  loop do
    sock = peer.accept
    _, target, = sock.gets.to_s.split(' ', 3)
    slices = []
    while (line = sock.gets)
      break if line.strip.empty?
      k, v = line.split(':', 2)
      slices << v.strip if k.to_s.strip.downcase == 'x-drift-slice'
    end
    body = JSON.generate({ 'path' => target.to_s.split('?', 2).first, 'slice' => slices.empty? ? nil : slices.join(',') })
    sock.write("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: #{body.bytesize}\r\nConnection: close\r\n\r\n#{body}")
    sock.close
  end
rescue IOError
  # The peer was closed once the cases that need it had run.
end
ENV.delete('DRIFT_SLICE')
ENV['DRIFT_LINK_BILLING_API_URL'] = "http://127.0.0.1:#{peer.addr[1]}/base/"
echoed = ->(resp) { JSON.parse(resp['body']) }

begin
  e = echoed.call(Drift::Edge.slice('billing-api').get('/api/events'))
  OUT['edge_slice_url'] = e['path']
  OUT['slice_identity_absent'] = e['slice']
rescue StandardError => e
  fail_with("Edge.slice get: #{e.message}")
end
begin
  OUT['slice_url'] = echoed.call(Drift.slice('billing-api').get('/api/events'))['path']
rescue StandardError => e
  fail_with("slice get: #{e.message}")
end

ENV['DRIFT_SLICE'] = 'billing'
begin
  resp = Drift::Edge.slice('billing-api').request('GET', '/api/events', headers: { 'X-Drift-Slice' => 'operator' })
  OUT['slice_identity_kept'] = echoed.call(resp)['slice']
rescue StandardError => e
  fail_with("Edge.slice request with a caller identity: #{e.message}")
end
ENV.delete('DRIFT_SLICE')
peer.close

begin
  Drift::Edge.slice('billing').get('/x')
  OUT['edge_slice_not_linked'] = nil
rescue StandardError => e
  OUT['edge_slice_not_linked'] = e.message
end
begin
  Drift.slice('billing').get('/x')
  OUT['slice_not_linked'] = nil
rescue StandardError => e
  OUT['slice_not_linked'] = e.message
end

emit
