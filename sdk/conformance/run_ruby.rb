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

emit
