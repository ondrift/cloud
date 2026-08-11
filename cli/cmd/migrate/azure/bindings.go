package azure

import (
	"encoding/json"
	"regexp"
	"strings"
)

// azBinding is one entry of an Azure function's function.json bindings[].
type azBinding struct {
	Type      string   `json:"type"`
	Direction string   `json:"direction"`
	Name      string   `json:"name"`
	Methods   []string `json:"methods,omitempty"`
	Route     string   `json:"route,omitempty"`
	AuthLevel string   `json:"authLevel,omitempty"`
	QueueName string   `json:"queueName,omitempty"`
	Schedule  string   `json:"schedule,omitempty"`
}

type azFunctionJSON struct {
	Bindings []azBinding `json:"bindings"`
}

var (
	azRouteParamRe  = regexp.MustCompile(`\{([^}?]+)\??\}`)
	nonIdentRe      = regexp.MustCompile(`[^a-z0-9]+`)
	collapseUnderRe = regexp.MustCompile(`_+`)
)

// azureRouteToDrift converts an Azure route template to Drift's form:
// "orders/{id}" → "orders/:id".
func azureRouteToDrift(route string) string {
	return azRouteParamRe.ReplaceAllString(route, ":$1")
}

// ncrontabToCron converts a 6-field NCRONTAB ("sec min hour dom mon dow") to a
// 5-field POSIX cron. ok=false when the seconds field carries meaning we can't
// represent in 5-field cron (anything but 0 or *) — the caller flags it.
func ncrontabToCron(s string) (cron string, ok bool) {
	f := strings.Fields(s)
	switch len(f) {
	case 5:
		return strings.Join(f, " "), true
	case 6:
		lossless := f[0] == "0" || f[0] == "*"
		return strings.Join(f[1:], " "), lossless
	default:
		return s, false
	}
}

// deriveTrigger parses function.json into a Drift trigger plus the list of
// non-trigger bindings (which become REPORT.md TODOs). ok=false when there's no
// supported trigger — the function is then refused.
func deriveTrigger(raw []byte, fnName string) (t ManifestTrigger, bindings []string, ok bool) {
	var fj azFunctionJSON
	if err := json.Unmarshal(raw, &fj); err != nil {
		return ManifestTrigger{}, nil, false
	}
	for _, b := range fj.Bindings {
		switch strings.ToLower(b.Type) {
		case "httptrigger":
			t.Type, t.Method, t.Auth = "http", "post", "none"
			if len(b.Methods) > 0 {
				t.Method = strings.ToLower(b.Methods[0])
			}
			if t.Route = azureRouteToDrift(b.Route); t.Route == "" {
				t.Route = slugify(fnName)
			}
			if al := strings.ToLower(b.AuthLevel); al == "function" || al == "admin" {
				t.Auth = "apikey"
			}
			ok = true
		case "queuetrigger":
			t.Type, t.Queue, t.Auth = "queue", b.QueueName, "none"
			ok = true
		case "timertrigger":
			cron, _ := ncrontabToCron(b.Schedule)
			t.Type, t.Schedule, t.Auth = "cron", cron, "none"
			ok = true
		default:
			if strings.HasSuffix(strings.ToLower(b.Type), "trigger") {
				bindings = append(bindings, "unsupported trigger: "+b.Type)
			} else {
				dir := b.Direction
				if dir == "" {
					dir = "io"
				}
				bindings = append(bindings, b.Type+" ("+dir+" binding \""+b.Name+"\")")
			}
		}
	}
	return t, bindings, ok
}

// driftHandlerName derives a valid Python identifier for the handler def.
func driftHandlerName(t ManifestTrigger, fnName string) string {
	var base string
	switch t.Type {
	case "http":
		base = t.Method + "_" + t.Route
	case "queue":
		base = "queue_" + t.Queue
	case "cron":
		base = "cron_" + fnName
	default:
		base = fnName
	}
	id := nonIdentRe.ReplaceAllString(strings.ToLower(base), "_")
	id = collapseUnderRe.ReplaceAllString(id, "_")
	id = strings.Trim(id, "_")
	if id == "" {
		id = "handler"
	}
	if id[0] >= '0' && id[0] <= '9' {
		id = "h_" + id
	}
	return id
}

// driftEntry renders one Driftfile `atomic.functions` entry for a trigger: the
// identity it answers on, the callable that serves it, the memory it starts
// with, its gate, and the secrets it may read.
//
// This is the whole declaration. Nothing is written into the scaffolded source,
// which is exactly what makes a migrated project readable — the code that comes
// out is the code that went in, and everything Drift needs to know about it is
// in one file the operator can review before deploying.
func driftEntry(t ManifestTrigger, handler string, secrets []string, memory string) string {
	var sb strings.Builder
	switch t.Type {
	case "http":
		sb.WriteString("    - name: \"" + t.Method + ":" + t.Route + "\"\n")
	case "queue":
		sb.WriteString("    - name: \"queue:" + t.Queue + "\"\n")
	case "cron":
		// A schedule is not an identity — it is something a function ALSO does.
		// The migrated function keeps a POST route and gains the cron beside it.
		sb.WriteString("    - name: \"post:" + handler + "\"\n")
	}
	sb.WriteString("      handler: " + handler + "\n")
	sb.WriteString("      memory: " + memory + "\n")
	if auth := t.Auth; auth != "" && auth != "none" {
		sb.WriteString("      auth: " + auth + "\n")
	}
	if t.Type == "cron" && t.Schedule != "" {
		sb.WriteString("      cron: \"" + t.Schedule + "\"\n")
	}
	if len(secrets) > 0 {
		sb.WriteString("      secrets: [" + strings.Join(secrets, ", ") + "]\n")
	}
	return sb.String()
}
