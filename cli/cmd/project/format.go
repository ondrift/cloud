package project

// format.go — the Driftfile format, described: every block, its labels, and
// every attribute, with what each means. `drift file explain` prints it.
//
// # The structure is read, the prose is written
//
// Which blocks exist, what labels they take and which attributes they hold is
// read from the structs driftfile.go decodes into, by their `hcl` tags, so
// what explain prints is exactly what the reader accepts. Only the prose is
// written here, keyed by the block's or attribute's path ("element.pulse",
// "element.pulse.memory"). TestFormat_DescribesEveryBlockAndAttribute holds the
// two together: a block or attribute with no description, or a description of
// nothing, fails it.

import (
	"reflect"
	"strings"
)

// FormatBlock is one block of the Driftfile format.
type FormatBlock struct {
	// Name is the block's type, "pulse"; "" for the file itself.
	Name string
	// Path is where it sits, "element.pulse"; "" for the file itself.
	Path string
	// Labels are what the block's labels mean, in order: ["trigger"].
	Labels []string
	Doc    string
	Attrs  []FormatAttr
	Blocks []FormatBlock
}

// FormatAttr is one attribute of a block.
type FormatAttr struct {
	Name, Type, Doc string
	Required        bool
}

// DriftfileFormat is the whole format, from the file down.
func DriftfileFormat() FormatBlock {
	return formatOf(reflect.TypeOf(dfFile{}), "", "")
}

func formatOf(t reflect.Type, name, path string) FormatBlock {
	b := FormatBlock{Name: name, Path: path, Doc: formatDocs[path].doc}
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("format") == "-" {
			continue // read only to be refused, so not part of the format
		}
		parts := strings.Split(t.Field(i).Tag.Get("hcl"), ",")
		if len(parts) != 2 || parts[0] == "" {
			continue // the def_range and label_range fields
		}
		key, kind := parts[0], parts[1]
		child := key
		if path != "" {
			child = path + "." + key
		}
		switch kind {
		case "label":
			b.Labels = append(b.Labels, key)
		case "optional":
			d := formatDocs[child]
			b.Attrs = append(b.Attrs, FormatAttr{Name: key, Type: d.typ, Doc: d.doc, Required: d.required})
		case "block":
			b.Blocks = append(b.Blocks, formatOf(t.Field(i).Type.Elem(), key, child))
		}
	}
	return b
}

// Find is the block or attribute at a dotted path, and the block it is in.
// An attribute comes back as a block holding only it.
func (b FormatBlock) Find(path string) (found *FormatBlock, attr *FormatAttr, parent *FormatBlock) {
	if path == "" {
		return &b, nil, nil
	}
	cur := b
	parts := strings.Split(path, ".")
	for i, part := range parts {
		next := -1
		for j, child := range cur.Blocks {
			if child.Name == part {
				next = j
			}
		}
		if next >= 0 {
			if i == len(parts)-1 {
				p := cur
				return &cur.Blocks[next], nil, &p
			}
			cur = cur.Blocks[next]
			continue
		}
		if i == len(parts)-1 {
			for j, a := range cur.Attrs {
				if a.Name == part {
					p := cur
					return nil, &cur.Attrs[j], &p
				}
			}
		}
		p := cur
		return nil, nil, &p
	}
	return nil, nil, nil
}

type formatDoc struct {
	typ      string
	doc      string
	required bool
}

// The types an attribute is written in.
const (
	tString   = "string"
	tSize     = `size ("32MiB": whole KiB, MiB or GiB)`
	tDuration = `duration ("30s", "24h", "7d")`
	tNumber   = "number"
	tBool     = "bool"
	tList     = "list of string"
	tMap      = "map of string"
	tRef      = `env("NAME")`
	tAny      = "string, number, bool, list or map"
)

// formatDocs is the prose of every block and attribute, by path.
var formatDocs = map[string]formatDoc{
	"": {doc: "A Driftfile declares one slice: what runs on it and its shape. Every booking sits beside " +
		"what it books and every storage size beside what it sizes, so the file is the whole of what " +
		"the slice is and costs. `drift plan` shows what applying it would change; `drift apply` applies it."},

	"slice": {doc: "The slice this file declares, by name: 1 to 32 lowercase letters, digits and hyphens. " +
		"`drift apply` creates it when it does not exist, and resizes it to this file's shape when it does."},
	"slice.labels": {typ: tMap, doc: "Your own labels for the slice, `team = \"backend\"`. They change no limit " +
		"and no price, `drift slice list` shows them, and a snapshot carries them."},
	"slice.limits": {doc: "The slice-wide part of the shape: everything a slice has that no single block in " +
		"this file owns. A limit left out is 0, which the platform enforces as the free slice's figure."},
	"slice.limits.requests_per_minute": {typ: tNumber, doc: "Requests a minute across the whole slice; more is " +
		"answered 429. The same limit as edge.rate_limit: write it in one place."},
	"slice.limits.timeout":          {typ: tDuration, doc: "Wall-clock time an invocation may run before it is killed."},
	"slice.limits.log_retention":    {typ: tDuration, doc: "How long invocation logs are kept, in whole hours."},
	"slice.limits.deploy_history":   {typ: tNumber, doc: "Past deploys kept per slot, for `drift atomic rollback`."},
	"slice.limits.backup_retention": {typ: tDuration, doc: "How long Backbone backups are kept, in whole days. Shortening it deletes the older backups on the next nightly run."},
	"slice.limits.scheduled_jobs": {typ: tNumber, doc: "Scheduled jobs the slice may run. Defaults to the " +
		"number of pulses with a cron, the fewest that runs them."},
	"slice.limits.atomic_storage":       {typ: tSize, doc: "Disk across every deployed slot: its code and vendored dependencies."},
	"slice.limits.canvas_storage":       {typ: tSize, doc: "Storage across every site the slice serves."},
	"slice.limits.realtime_connections": {typ: tNumber, doc: "Live Realtime connections held at once; 0 turns Realtime off."},
	"slice.limits.secrets":              {typ: tNumber, doc: "Secrets the slice may hold."},
	"slice.limits.secret_size":          {typ: tSize, doc: "Largest value one secret may hold."},
	"slice.limits.locks":                {typ: tNumber, doc: "Backbone locks held at once."},
	"slice.limits.blob_object_size":     {typ: tSize, doc: "Largest single object a bucket accepts."},
	"slice.limits.queue_depth":          {typ: tNumber, doc: "Messages a queue holds when its own block states no depth."},
	"slice.limits.vault_entry_size":     {typ: tSize, doc: "Largest value one Deed Vault entry may hold."},
	"slice.limits.vault_entries":        {typ: tNumber, doc: "Deed Vault entries one identity may hold."},
	"slice.limits.pocket_record_size":   {typ: tSize, doc: "Largest record a Deed Pocket accepts."},

	"atomic": {doc: "The Atomic pillar: every element the slice runs, and the pulses and fields each one " +
		"declares. A slice has one atomic block; a file without one runs no Atomic code."},
	"atomic.element": {doc: "A folder of one language's source and its one dependency manifest. \"default\" is " +
		"./atomic itself; any other name is ./atomic/<name>. Each pulse and field is declared inside the " +
		"element its handler lives in."},
	"atomic.element.dir": {typ: tString, doc: "The element's folder, relative to the Driftfile, when it is not the default one."},
	"atomic.element.pulse": {doc: "A Pulse answers one call with one result. It is labelled with its trigger: an HTTP " +
		"method and route, \"POST /rpc\", or QUEUE and the queue it drains, \"QUEUE orders\". Its booking key is " +
		"the method and route, unique across the slice."},
	"atomic.element.pulse.handler": {typ: tString, required: true, doc: "The callable in the element's source that " +
		"serves it: `PostRPC`, `handle_order`, `createBoard`."},
	"atomic.element.pulse.memory": {typ: tSize, required: true, doc: "The memory it books, from 8MiB to 256MiB (16MiB in " +
		"a compiled language). It is the pulse's own pool and what it is billed at; `drift size` rewrites it " +
		"from what the slice measured."},
	"atomic.element.pulse.response": {typ: tString, doc: `How replies are written: "envelope" (the default, ` +
		`{status, message, payload}), "json" for the payload as the whole body, or "raw" for bytes.`},
	"atomic.element.pulse.concurrent": {typ: tBool, doc: "Whether the handler is safe to run in parallel. true lets one " +
		"warm process serve many calls at once; how many is the slice's to decide."},
	"atomic.element.pulse.stream": {typ: tString, doc: `Hold the connection open: "sse" for Server-Sent Events, ` +
		`"ws" for WebSocket.`},
	"atomic.element.pulse.cron": {typ: tString, doc: "A five-field cron expression that also runs the pulse on a " +
		"schedule. An HTTP pulse with a cron stays callable over HTTP."},
	"atomic.element.pulse.env": {typ: tMap, doc: "Plain configuration injected into its environment. Not for " +
		"credentials: those are secret blocks, named in `secrets`."},
	"atomic.element.pulse.secrets": {typ: tList, doc: "The secrets this pulse may read. One the list does not name is " +
		"unreachable from it, though the slice holds it."},
	"atomic.element.pulse.dir":  {typ: tString, doc: "The folder its handler is in, when it is not its element's."},
	"atomic.element.pulse.auth": {doc: "An API key gate: the pulse answers only a caller presenting the key."},
	"atomic.element.pulse.auth.api_key": {typ: tRef, required: true, doc: "The environment variable the key is read " +
		"from. Written env(\"NAME\") and nothing else, so the file never holds the key."},
	"atomic.element.field": {doc: "A Field is a long-running worker: the slice keeps exactly one running, starts it on " +
		"every deploy and restarts it when it exits. Fields run Go. Its booking key is field:<name>."},
	"atomic.element.field.handler": {typ: tString, required: true, doc: "The callable it runs, " +
		"`func Name(ctx context.Context) error`."},
	"atomic.element.field.memory": {typ: tSize, required: true, doc: "The memory it books: 16MiB or more, since a " +
		"field runs Go. What it is billed at."},
	"atomic.element.field.env":     {typ: tMap, doc: "Plain configuration injected into its environment."},
	"atomic.element.field.secrets": {typ: tList, doc: "The secrets this field may read."},
	"atomic.element.field.dir":     {typ: tString, doc: "The folder its handler is in, when it is not its element's."},

	"backbone": {doc: "The Backbone pillar: every database, collection, bucket, queue, cache entry and secret " +
		"the slice holds. A slice has one backbone block; a file without one holds no Backbone primitive."},
	"backbone.sql": {doc: "A SQLite database, reached as drift.SQL(\"name\")."},
	"backbone.sql.size": {typ: tSize, required: true, doc: "The database's storage: the enforced limit and what it is " +
		"billed at."},
	"backbone.sql.schema": {typ: tString, doc: "A file of idempotent DDL, applied on every deploy before the migrations."},
	"backbone.sql.migrations": {typ: tString, doc: "A folder of numbered migrations, `0001_create_users.sql`, each " +
		"applied once, in order."},
	"backbone.sql.seed": {typ: tString, doc: "A SQL file run after the migrations while the database holds no data."},

	"backbone.nosql": {doc: "A NoSQL document collection."},
	"backbone.nosql.size": {typ: tSize, required: true, doc: "The collection's storage: the enforced limit and what it " +
		"is billed at. A write past it is refused."},
	"backbone.nosql.seed": {typ: tString, doc: "A JSONL file, one document per line, each with an _id, upserted on deploy."},
	"backbone.nosql.ttl":  {typ: tDuration, doc: "How long a document lives after its last write."},
	"backbone.nosql.unique": {typ: tList, doc: "Fields whose values must be unique in the collection; a write that " +
		"repeats one is refused."},

	"backbone.blobs":      {doc: "A bucket in the slice's blob store."},
	"backbone.blobs.size": {typ: tSize, required: true, doc: "The bucket's storage: the enforced limit and what it is billed at."},

	"backbone.queue": {doc: "A FIFO queue. A pulse labelled QUEUE <name> drains it."},
	"backbone.queue.depth": {typ: tNumber, doc: "Messages it holds before producers are throttled. Left out, the " +
		"slice's queue_depth."},

	"backbone.cache":       {doc: "A cache entry, by key, written on every deploy."},
	"backbone.cache.file":  {typ: tString, doc: "A file whose contents are the value."},
	"backbone.cache.value": {typ: tString, doc: "The value itself."},
	"backbone.cache.ttl":   {typ: tNumber, doc: "Seconds the entry lives."},

	"backbone.secret": {doc: "A secret the slice holds, named like an environment variable: STRIPE_KEY. A pulse or " +
		"field reads it only when its `secrets` names it."},
	"backbone.secret.value": {typ: tRef, required: true, doc: "The environment variable its value is read from at " +
		"apply. Written env(\"NAME\") and nothing else, so the file never holds a credential."},

	"backbone.realtime": {doc: "Realtime, the WebSocket pub/sub of the slice. A backbone block has one realtime " +
		"block; a file without one opens no channel to visitors."},
	"backbone.realtime.client_channels": {typ: tList, doc: "The channels a visitor's browser may publish to, the whole " +
		"list: a channel name (\"chat\"), a prefix ending in * (\"room:*\"), or \"*\" for every channel. A frame " +
		"a client sends on any other channel is dropped and never reaches the room. A pulse always may publish; " +
		"this governs clients only. Empty closes every channel; at most 64 entries."},

	"canvas": {doc: "The Canvas pillar: every static site the slice serves. A slice has one canvas block; a " +
		"file without one serves no site."},
	"canvas.site":     {doc: "A static site, labelled with the path it is served at: \"/\" for the root."},
	"canvas.site.dir": {typ: tString, required: true, doc: "The folder it serves, relative to the Driftfile."},

	"edge": {doc: "What can reach the slice, and what it can reach. An attribute or block left out leaves " +
		"that part of the slice as it is."},
	"edge.rate_limit": {typ: tString, doc: `Requests across the slice per window, "100/s", "6000/min". The ` +
		`same limit as limits.requests_per_minute: write it in one place.`},
	"edge.links": {typ: tList, doc: "Your other slices this one may call and front. When written, it is the " +
		"whole list."},
	"edge.allow": {typ: tList, doc: "Inbound CIDRs. When it names any, only those addresses reach the slice; " +
		"empty admits everyone."},
	"edge.hosts": {typ: tMap, doc: "Hosts this slice fronts for slices it links: host = slice. Each host is " +
		"a verified domain of this slice."},
	"edge.egress": {doc: "Where the slice may call out to."},
	"edge.egress.mode": {typ: tString, doc: `"open" for any public host, or "allowlist" for the hosts listed ` +
		`and no other.`},
	"edge.egress.hosts": {typ: tList, doc: "The hosts an allowlist admits, \"host\" or \"host:port\"."},
	"edge.domain": {doc: "A custom hostname the slice answers on, proved yours by a DNS TXT record before it " +
		"routes."},
	"edge.domain.wildcard": {typ: tBool, doc: "Route every subdomain of the host to this slice as well."},
	"edge.site_auth":       {doc: "A password gate in front of the slice's sites, or the part of them `paths` names. Its pulses are not gated."},
	"edge.site_auth.realm": {typ: tString, doc: "The realm the browser shows when it asks for the password."},
	"edge.site_auth.paths": {typ: tList, required: true, doc: `The Canvas paths the gate covers: ["*"] for the ` +
		`whole site, or absolute paths, each covering itself and everything beneath it ("/admin" gates /admin and ` +
		`/admin/users/1, not /administrator). A path outside the list is served without a password.`},
	"edge.site_auth.user": {doc: "One user the gate admits."},
	"edge.site_auth.user.password": {typ: tRef, required: true, doc: "The environment variable the password " +
		"is read from. Written env(\"NAME\") and nothing else."},

	"variable": {doc: "A value the file reads as var.<name>, so one figure can differ per environment."},
	"variable.default": {typ: tAny, doc: "Its value when the environment being planned sets none. A variable " +
		"with no default must be set by every environment that is planned."},

	"environment": {doc: "Another slice from the same file: `drift plan <name>` and `drift apply <name>` " +
		"work on it. Its slice is <slice>-<name> unless it names one; \"prod\" and \"production\" are " +
		"the slice itself."},
	"environment.slice": {typ: tString, doc: "The slice this environment is applied to."},
	"environment.vars":  {typ: tMap, doc: "The variables it sets, by name."},

	"hook": {doc: "A local command run around `drift apply`, labelled with when: \"pre_deploy\" before " +
		"anything is sent, \"post_deploy\" once the slice is live."},
	"hook.run": {typ: tString, required: true, doc: "The shell command."},

	"test": {doc: "A command `drift file test` runs against the project running locally; DRIFT_TEST_URL " +
		"says where."},
	"test.run": {typ: tString, required: true, doc: "The shell command."},
}
