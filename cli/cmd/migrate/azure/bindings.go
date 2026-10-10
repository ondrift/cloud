package azure

import (
	"encoding/json"
	"fmt"
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

// driftPulse renders one Driftfile pulse block for a trigger, indented to sit
// inside its element: the trigger it answers on, the callable that serves it,
// the memory it starts with, its gate, and the secrets it may read. keyVar is
// the environment variable an API-key gate reads its key from.
//
// This is the whole declaration. Nothing is written into the scaffolded source,
// which is exactly what makes a migrated project readable — the code that comes
// out is the code that went in, and everything Drift needs to know about it is
// in one file the operator can review before deploying.
func driftPulse(t ManifestTrigger, handler string, secrets []string, memory, keyVar string) string {
	var label string
	switch t.Type {
	case "http":
		label = strings.ToUpper(t.Method) + " /" + t.Route
	case "queue":
		label = "QUEUE " + t.Queue
	case "cron":
		// A schedule is not an identity — it is something a pulse ALSO does.
		// The migrated pulse keeps a POST route and gains the cron beside it.
		label = "POST /" + handler
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "  pulse %q {\n", label)
	fmt.Fprintf(&sb, "    handler = %q\n", handler)
	fmt.Fprintf(&sb, "    memory  = %q\n", memory)
	if t.Type == "cron" && t.Schedule != "" {
		fmt.Fprintf(&sb, "    cron    = %q\n", t.Schedule)
	}
	if len(secrets) > 0 {
		quoted := make([]string, len(secrets))
		for i, s := range secrets {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		fmt.Fprintf(&sb, "    secrets = [%s]\n", strings.Join(quoted, ", "))
	}
	if auth := t.Auth; auth != "" && auth != "none" {
		sb.WriteString("    auth {\n")
		fmt.Fprintf(&sb, "      api_key = env(%q)\n", keyVar)
		sb.WriteString("    }\n")
	}
	sb.WriteString("  }\n")
	return sb.String()
}
