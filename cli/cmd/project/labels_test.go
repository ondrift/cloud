package project

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// labelled builds the live slice document the reconcile reads.
func labelled(labels map[string]string) *LiveSlice {
	l := &LiveSlice{}
	l.Config.Labels = labels
	return l
}

// declaring builds a manifest whose Driftfile carries `labels:`.
//
// Through manifestFrom, so it never passes the schema — which is deliberate
// here: the schema gains `labels` with cloud/platform#656, and these tests are
// about the reconcile rather than about what the platform is currently serving.
func declaring(labels map[string]string) *Manifest {
	doc := Node{"slice": "demo"}
	if labels != nil {
		m := map[string]any{}
		for k, v := range labels {
			m[k] = v
		}
		doc["labels"] = m
	}
	return manifestFrom(doc)
}

// THE DIFF IS THE LOGIC, so it is tested as a value rather than through a
// deploy. Everything else in this file is plumbing around it.
func TestDiffLabels(t *testing.T) {
	for _, tc := range []struct {
		what              string
		declared, current map[string]string
		want              []string
	}{
		{"nothing anywhere", nil, nil, nil},
		{"unchanged is not a change",
			map[string]string{"team": "backend"}, map[string]string{"team": "backend"}, nil},
		{"added",
			map[string]string{"team": "backend"}, nil, []string{"+ team=backend"}},
		{"REMOVED — the case the Driftfile is the only way to express",
			nil, map[string]string{"team": "backend"}, []string{"- team (was backend)"}},
		{"changed",
			map[string]string{"env": "prod"}, map[string]string{"env": "staging"},
			[]string{"~ env=prod (was staging)"}},
		{"one of each, in key order",
			map[string]string{"env": "prod", "team": "backend"},
			map[string]string{"client": "acme", "env": "staging"},
			[]string{"- client (was acme)", "~ env=prod (was staging)", "+ team=backend"}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			got := diffLabels(tc.declared, tc.current)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d changes, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, c := range got {
				if describeLabelChange(c) != tc.want[i] {
					t.Errorf("change %d = %q, want %q", i, describeLabelChange(c), tc.want[i])
				}
			}
		})
	}
}

// A value that is legal here and illegal as a Kubernetes label. These never
// reach cluster metadata, and the reconcile must not quietly normalise one.
func TestDiffLabels_CarriesAValueKubernetesWouldRefuse(t *testing.T) {
	got := diffLabels(map[string]string{"client": "bakkerij & zn."}, nil)
	if len(got) != 1 || got[0].To != "bakkerij & zn." {
		t.Fatalf("got %+v — the value was altered on the way through", got)
	}
}

// IN SYNC MEANS SILENT, and it means no request at all.
//
// This is the common case on every deploy of every project — most Driftfiles
// declare no labels and most slices carry none — so a reconcile that called the
// resize endpoint anyway would put a write on the deploy path of every apply in
// existence, for nothing.
func TestApplyLabels_InSyncIssuesNoRequestAndPrintsNothing(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	var out strings.Builder
	m := declaring(map[string]string{"team": "backend"})
	if err := applyLabels(m, labelled(map[string]string{"team": "backend"}), &out); err != nil {
		t.Fatalf("applyLabels: %v", err)
	}

	if n := rec.count("POST /ops/slice/resize"); n != 0 {
		t.Errorf("issued %d resizes for labels that already match", n)
	}
	if out.String() != "" {
		t.Errorf("printed %q with nothing to report", out.String())
	}
}

// REMOVING THE LAST LABEL, which is the half a Driftfile is the only way to
// express: there is nowhere else to say "and none of the others".
//
// The body must carry `labels` as an EMPTY OBJECT. Omitting the key would mean
// "say nothing about labels" under patch semantics, and the slice would keep the
// ones the Driftfile stopped naming — the removal silently doing nothing.
func TestApplyLabels_DroppingThemSendsAnEmptySet(t *testing.T) {
	var body []byte
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/slice/resize" {
			body, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	var out strings.Builder
	// A Driftfile with no labels block at all, against a slice carrying two.
	if err := applyLabels(declaring(nil),
		labelled(map[string]string{"team": "backend", "env": "staging"}), &out); err != nil {
		t.Fatalf("applyLabels: %v", err)
	}

	if n := rec.count("POST /ops/slice/resize"); n != 1 {
		t.Fatalf("issued %d resizes, want exactly 1", n)
	}

	var sent struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("the request body does not parse: %v (%s)", err, body)
	}
	raw, ok := sent.Config["labels"]
	if !ok {
		t.Fatal("the patch omitted `labels` — under patch semantics that means " +
			"\"say nothing about them\", so the slice would KEEP the labels the " +
			"Driftfile stopped naming")
	}
	if string(raw) != "{}" {
		t.Errorf("labels = %s, want {} — an empty set is how the last one is removed", raw)
	}

	// And ONLY labels. A patch carrying anything else would be a deploy changing
	// a slice's shape, which is the thing apply may never do.
	if len(sent.Config) != 1 {
		t.Errorf("the patch carried %d config keys, want only `labels`: %v",
			len(sent.Config), sent.Config)
	}

	if !strings.Contains(out.String(), "- team (was backend)") ||
		!strings.Contains(out.String(), "- env (was staging)") {
		t.Errorf("the report does not name both removals:\n%s", out.String())
	}
}

// Adding sends exactly what the Driftfile declares.
func TestApplyLabels_AddingSendsTheDeclaredSet(t *testing.T) {
	var body []byte
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/slice/resize" {
			body, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	var out strings.Builder
	m := declaring(map[string]string{"team": "backend", "client": "bakkerij & zn."})
	if err := applyLabels(m, labelled(nil), &out); err != nil {
		t.Fatalf("applyLabels: %v", err)
	}

	var sent struct {
		Config struct {
			Labels map[string]string `json:"labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("the request body does not parse: %v (%s)", err, body)
	}
	if sent.Config.Labels["client"] != "bakkerij & zn." || len(sent.Config.Labels) != 2 {
		t.Errorf("sent %+v, want both labels with the value unaltered", sent.Config.Labels)
	}
}

// A slice apply could not read is left alone rather than having its labels
// cleared. `live` is nil when the record could not be fetched, and treating that
// as "it has no labels" would delete every label on the slice for a failed read.
func TestApplyLabels_AnUnreadableSliceChangesNothing(t *testing.T) {
	rec := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	var out strings.Builder
	if err := applyLabels(declaring(nil), nil, &out); err != nil {
		t.Fatalf("applyLabels: %v", err)
	}
	if n := rec.count("POST /ops/slice/resize"); n != 0 {
		t.Errorf("issued %d resizes against a slice it could not read", n)
	}
}
