package project

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A domain the Driftfile declares with `wildcard = true` is added as a
// wildcard: every subdomain routes to the slice. The compiler carries the
// attribute into the document, so the add is where it has to reach the
// platform, the same field `drift slice domain add --wildcard` sends.
func TestApplyDomains_AWildcardDomainIsAddedAsOne(t *testing.T) {
	withRealSchema(t)
	c, err := CompileDriftfile(writeDriftfile(t, "slice \"demo\" {\n  billing = \"monthly\"\n}\n\n"+
		"edge {\n  domain \"shop.example.com\" {\n    wildcard = true\n  }\n\n  domain \"plain.example.com\" {}\n}\n"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	added := map[string]map[string]any{}
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/slice/domain" && r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			host, _ := body["host"].(string)
			added[host] = body
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})

	if _, err := applyDomains(c.Manifest); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("added %d domains, want both declared ones: %v", len(added), added)
	}
	if added["shop.example.com"]["wildcard"] != true {
		t.Errorf("shop.example.com is declared wildcard and was added with %v", added["shop.example.com"])
	}
	if w, ok := added["plain.example.com"]["wildcard"]; ok && w != false {
		t.Errorf("plain.example.com is not declared wildcard and was added with %v", added["plain.example.com"])
	}
}
