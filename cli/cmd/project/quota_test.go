package project

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// The envelope as the platform serves it, including the three things a
// client-side mirror cannot express: the declaration flag, the per-queue
// depths, and Deed's caps.
const sliceQuotaBody = `{"declaration_model":true,"no_sql_collection_quotas":{"events":104857600},` +
	`"blob_bucket_quotas":{"uploads":524288000},"sql_database_quotas":{"ledger":52428800},` +
	`"queue_quotas":{"orders":500},"vault_max_size_bytes":4096,"pocket_max_size_bytes":16384}`

// quotaStub answers /ops/slice/quota and records the slice the caller asked
// about, which is the one thing the shared recorder cannot see.
func quotaStub(t *testing.T, status int, body string) *string {
	t.Helper()
	asked := new(string)
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ops/slice/quota" {
			*asked = r.Header.Get("X-Slice")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	return asked
}

// The bytes reach the container exactly as the platform produced them. Decoding
// and re-encoding here would make the CLI a second author of a wire contract,
// and the fields it has no struct for would vanish on the way through.
func TestLocalQuotaConfig_ForwardsThePlatformsBytesUnchanged(t *testing.T) {
	quotaStub(t, http.StatusOK, sliceQuotaBody)

	var out bytes.Buffer
	got := localQuotaConfig("lab", &out)

	if got != sliceQuotaBody {
		t.Errorf("the envelope must be forwarded byte-for-byte.\n got: %s\nwant: %s", got, sliceQuotaBody)
	}
	if out.Len() != 0 {
		t.Errorf("a successful read is silent, got %q", out.String())
	}
}

// The manifest names the slice being run. Reading it off the session instead
// asks the platform about whatever this machine last used, which is how the
// orphan check spent its whole life asking about "default".
func TestLocalQuotaConfig_AsksAboutTheSliceTheManifestNames(t *testing.T) {
	asked := quotaStub(t, http.StatusOK, sliceQuotaBody)

	localQuotaConfig("invoices", &bytes.Buffer{})

	if *asked != "invoices" {
		t.Errorf("the platform was asked about %q, want the manifest's slice %q", *asked, "invoices")
	}
}

// `drift file run` needs no account and no network, and that survives: an
// unreadable slice costs one stated line, not the run.
func TestLocalQuotaConfig_StartsAnywayAndSaysSoWhenTheSliceCannotBeRead(t *testing.T) {
	quotaStub(t, http.StatusInternalServerError, `{"message":"mongo unreachable"}`)

	var out bytes.Buffer
	got := localQuotaConfig("lab", &out)

	if got != "" {
		t.Errorf("an unreadable slice yields no limits, got %q", got)
	}
	said := out.String()
	if said == "" {
		t.Fatal("an unenforced run must say it is unenforced — silence here is the bug being fixed")
	}
	if !strings.Contains(said, "would refuse") || !strings.Contains(said, "accepted here") {
		t.Errorf("the line must name what is not enforced, got %q", said)
	}
	if !strings.Contains(said, "lab") {
		t.Errorf("the line must name the slice whose limits are missing, got %q", said)
	}
}

// The control: the limits reach the container's argv, which is the only place
// the difference is visible before a process exists.
func TestLocalRunArgs_HandsTheContainerTheSlicesOwnLimits(t *testing.T) {
	m := manifestFrom(Node{"slice": "lab"})

	args := localRunArgs(m, "drift-lab", "drift-run-lab", 8002, false, sliceQuotaBody)

	want := "DRIFT_QUOTA_CONFIG=" + sliceQuotaBody
	if !containsArg(args, want) {
		t.Errorf("the argv must carry the envelope, got %v", args)
	}
	if !containsArg(args, "DRIFT_STANDALONE_SAT=drift-run") {
		t.Error("the SAT must still be passed")
	}
	if args[len(args)-1] != "drift-run-lab" {
		t.Errorf("the image must stay last, got %q", args[len(args)-1])
	}
}

// The other control: no limits means no variable at all, rather than an empty
// one. Without this the test above passes for code that always appends the flag.
func TestLocalRunArgs_OmitsTheVariableWhenNoLimitsWereRead(t *testing.T) {
	m := manifestFrom(Node{"slice": "lab"})

	args := localRunArgs(m, "drift-lab", "drift-run-lab", 8002, false, "")

	for _, a := range args {
		if strings.HasPrefix(a, "DRIFT_QUOTA_CONFIG") {
			t.Errorf("no limits were read, so no variable must be passed, got %q", a)
		}
	}
}

// Declared secrets still ride in beside the limits.
func TestLocalRunArgs_KeepsTheDeclaredSecrets(t *testing.T) {
	m := manifestFrom(Node{"slice": "lab", "backbone": map[string]any{
		"secrets": map[string]any{"STRIPE_KEY": "sk_test_1"},
	}})

	args := localRunArgs(m, "drift-lab", "drift-run-lab", 8002, true, sliceQuotaBody)

	if !containsArg(args, "DRIFT_SECRET_STRIPE_KEY=sk_test_1") {
		t.Errorf("a declared secret must still be passed, got %v", args)
	}
	if !containsArg(args, "drift-lab-data:/data") {
		t.Errorf("--persist must still mount the volume, got %v", args)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
