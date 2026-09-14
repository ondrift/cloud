// export.go — `drift account export`. Everything this account can read, in one
// archive, without doing it by hand.
//
// # It reads NOTHING a person could not already read
//
// There is no export route on the platform and deliberately so. This command
// calls the same `/ops/*` endpoints `drift backbone nosql list`, `drift backbone
// blob get`, `drift deed vault list` and the rest already call, with the
// caller's own token. So the archive's contents are bounded by what the account
// is entitled to BY CONSTRUCTION rather than by a server-side rule somebody has
// to keep correct — and a scope the token does not carry fails here exactly as
// it would at the command line.
//
// That is also why this is the whole feature. "Can we get our data out" is a
// question about assembly, not about access: every byte was already reachable,
// one command at a time, across however many slices and collections an account
// has. What was missing was the walk.
//
// # Secret VALUES are not in it unless you say so, twice
//
// A leaked export must not be equivalent to a leaked credential store. Secret
// NAMES are in by default — they are a map of what the tenant integrates with,
// which is theirs — and the values need `--include-secret-values` plus typing
// the account name back. Same shape as `drift account delete`'s confirmation,
// for the same reason: the cost of getting it wrong is not recoverable.
package account

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ondrift/cloud/cli/cmd/project"
	sliceCmd "github.com/ondrift/cloud/cli/cmd/slice"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// exportPageSize is the page a collection is walked in. The platform clamps a
// list to 1000, so asking for more buys nothing and asking for less buys round
// trips.
const exportPageSize = 1000

// sliceGet fetches one path against a NAMED slice rather than the active one.
//
// Every `/ops/*` route reads `X-Slice` to decide which slice it addresses, and
// `newAuthenticatedRequestCtx` fills it from the session. An export walks every
// slice the account has, so it has to say which — otherwise it would read the
// active slice's collections once per slice and write the same data under each
// name, which is a wrong archive that looks complete.
func sliceGet(sliceName, url string, what string) ([]byte, error) {
	resp, err := common.DoRequestWithHeaders(http.MethodGet, url, nil,
		map[string]string{"X-Slice": sliceName})
	if err != nil {
		return nil, common.TransportError(what, err)
	}
	defer resp.Body.Close()
	return common.CheckResponse(resp, what)
}

// exportWriter collects what went into the archive and what did not.
//
// A PARTIAL EXPORT IS NOT A FAILED ONE. A single unreadable bucket must not
// abandon an otherwise complete archive of everything else — but it must not be
// silent either, or the archive claims to be the account's data and quietly is
// not. Every skip is counted, printed at the end, and written into the
// manifest, so the file itself carries the same caveat the terminal did.
type exportWriter struct {
	zw       *zip.Writer
	root     string
	files    int
	bytes    int64
	problems []string
}

func (e *exportWriter) add(name string, body []byte) {
	w, err := e.zw.Create(path.Join(e.root, name))
	if err != nil {
		e.problems = append(e.problems, fmt.Sprintf("%s: %v", name, err))
		return
	}
	n, err := w.Write(body)
	if err != nil {
		e.problems = append(e.problems, fmt.Sprintf("%s: %v", name, err))
		return
	}
	e.files++
	e.bytes += int64(n)
}

func (e *exportWriter) addJSON(name string, v any) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		e.problems = append(e.problems, fmt.Sprintf("%s: %v", name, err))
		return
	}
	e.add(name, append(body, '\n'))
}

func (e *exportWriter) skip(what string, err error) {
	e.problems = append(e.problems, fmt.Sprintf("%s: %v", what, err))
}

func GetExportCmd() *cobra.Command {
	var out string
	var includeSecretValues bool

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Download everything in this account as one archive",
		Long: `Download everything in this account as one archive.

Walks every slice you own and writes its shape, its NoSQL documents, its blobs,
its secret NAMES and its Deed records into a single zip.

It reads nothing you could not already read yourself — it calls the same
endpoints 'drift backbone nosql list', 'drift backbone blob get' and
'drift deed vault list' call, with your own credentials. What it adds is the
walk.

SECRET VALUES ARE NOT INCLUDED unless you pass --include-secret-values and
confirm. A leaked archive should not be the same thing as a leaked credential
store.`,
		Example: "  drift account export\n" +
			"  drift account export --out ~/drift-backup.zip\n" +
			"  drift account export --include-secret-values",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			username := common.GetUsername()
			if username == "" {
				e := fmt.Errorf("Couldn't export: you're not logged in. Run `drift account login` first.")
				return e
			}

			// The values are asked for BEFORE any work, so a refusal costs
			// nothing and a confirmation is not buried under a progress log.
			if includeSecretValues {
				fmt.Println("--include-secret-values puts every secret's PLAINTEXT in this archive.")
				fmt.Println("  Anyone who reads the file can read those credentials, including ones")
				fmt.Println("  for systems that are not Drift.")
				typed := strings.TrimSpace(common.PromptForInput(
					fmt.Sprintf("Type %q to include them", username)))
				if typed != username {
					fmt.Println("Not confirmed — exporting secret NAMES only.")
					includeSecretValues = false
				}
			}

			stamp := time.Now().UTC().Format("2006-01-02")
			if out == "" {
				out = fmt.Sprintf("drift-export-%s-%s.zip", username, stamp)
			}

			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				e := fmt.Errorf("Couldn't export: could not create %s (%v)", out, err)
				return e
			}
			defer f.Close()

			// STREAMED into the file rather than assembled in memory. An account
			// with real blobs in it is an archive nobody wants to hold twice, and
			// the previous helper here (ZipFolder) builds a bytes.Buffer of the
			// whole thing.
			zw := zip.NewWriter(f)
			e := &exportWriter{zw: zw, root: fmt.Sprintf("drift-export-%s-%s", username, stamp)}

			slices, err := sliceCmd.FetchSlices()
			if err != nil {
				_ = zw.Close()
				return err
			}

			e.addJSON("account.json", map[string]any{
				"username":    username,
				"exported_at": time.Now().UTC().Format(time.RFC3339),
				"slices":      len(slices),
				// No password hash, and nothing about how the account is
				// protected. An export is the tenant's DATA; the credential
				// material that guards it is not data they need a copy of.
			})

			for _, s := range slices {
				fmt.Printf("  %s\n", s.Name)
				exportSlice(e, s, includeSecretValues)
			}

			e.add("README.txt", []byte(readme(username, stamp, includeSecretValues, e.problems)))

			if err := zw.Close(); err != nil {
				er := fmt.Errorf("Couldn't export: finishing the archive failed (%v)", err)
				fmt.Println(er)
				return er
			}

			fmt.Printf("\n%s %s\n", common.Highlight("✓"), out)
			fmt.Printf("  %d file(s), %s\n", e.files, humanBytes(e.bytes))
			if includeSecretValues {
				fmt.Printf("  %s\n", "CONTAINS SECRET VALUES IN PLAINTEXT — treat it as a credential.")
			} else {
				fmt.Printf("  secret names only (no values)\n")
			}
			// THE INCOMPLETE CASE IS SAID OUT LOUD. An archive that quietly
			// omits a bucket is worse than one that failed, because it will be
			// trusted.
			if len(e.problems) > 0 {
				fmt.Printf("\n  %d thing(s) could not be read and are NOT in the archive:\n",
					len(e.problems))
				for _, p := range e.problems {
					fmt.Printf("    - %s\n", p)
				}
				fmt.Printf("  They are listed in README.txt inside the archive too.\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&out, "out", "", "where to write the archive (default: ./drift-export-<account>-<date>.zip)")
	cmd.Flags().BoolVar(&includeSecretValues, "include-secret-values", false,
		"include secret PLAINTEXT (asks for confirmation; the archive becomes a credential)")
	return cmd
}

// exportSlice writes one slice's whole contents into the archive.
func exportSlice(e *exportWriter, s sliceCmd.SliceEntry, includeSecretValues bool) {
	base := path.Join("slices", s.Name)

	// The slice's own shape, which is what says how to recreate it — and what
	// names the collections and buckets everything below walks.
	live, err := project.FetchLiveSlice(s.Name)
	if err != nil || live == nil {
		e.skip(fmt.Sprintf("slice %s: shape", s.Name), orErr(err, "not found"))
		return
	}
	e.addJSON(path.Join(base, "slice.json"), live)

	for name := range live.Config.Backbone.NoSQL.Collections {
		exportCollection(e, s.Name, base, name)
	}
	for bucket := range live.Config.Backbone.Blobs.Buckets {
		exportBucket(e, s.Name, base, bucket)
	}
	exportSecrets(e, s.Name, base, includeSecretValues)
	exportDeed(e, s.Name, base)
}

// exportCollection pages a collection to exhaustion.
//
// `--all`'s walk, not `list`'s single page: one request returns at most 1000
// documents and says nothing about being short, so exporting a page would
// silently truncate every collection bigger than that — and rows come back in
// key order rather than newest-first, so what it dropped would be arbitrary.
func exportCollection(e *exportWriter, sliceName, base, collection string) {
	var all []json.RawMessage
	after := ""
	for {
		u := fmt.Sprintf("%s/ops/backbone/nosql/list?collection=%s&limit=%d",
			common.APIBaseURL, url.QueryEscape(collection), exportPageSize)
		if after != "" {
			u += "&after=" + url.QueryEscape(after)
		}
		body, err := sliceGet(sliceName, u, "export documents")
		if err != nil {
			e.skip(fmt.Sprintf("%s/%s: documents", sliceName, collection), err)
			return
		}
		var page []json.RawMessage
		if err := json.Unmarshal(body, &page); err != nil {
			e.skip(fmt.Sprintf("%s/%s: documents", sliceName, collection), err)
			return
		}
		all = append(all, page...)
		if len(page) < exportPageSize {
			break
		}
		next := storageKeyOf(page[len(page)-1])
		if next == "" || next == after {
			// A full page that does not move the cursor would loop forever.
			// Recorded rather than silently truncated: this archive is now
			// incomplete for this collection and has to say so.
			e.skip(fmt.Sprintf("%s/%s: documents", sliceName, collection),
				fmt.Errorf("paging stopped advancing after %d documents", len(all)))
			break
		}
		after = next
	}
	e.addJSON(path.Join(base, "nosql", collection+".json"), all)
}

func storageKeyOf(doc json.RawMessage) string {
	var d struct {
		Key string `json:"_key"`
	}
	if json.Unmarshal(doc, &d) != nil {
		return ""
	}
	return d.Key
}

// exportBucket writes every object in a bucket, as the bytes they are.
func exportBucket(e *exportWriter, sliceName, base, bucket string) {
	body, err := sliceGet(sliceName,
		fmt.Sprintf("%s/ops/backbone/blob/list?bucket=%s",
			common.APIBaseURL, url.QueryEscape(bucket)),
		"export blob keys")
	if err != nil {
		e.skip(fmt.Sprintf("%s/%s: blob keys", sliceName, bucket), err)
		return
	}
	var keys []string
	if err := json.Unmarshal(body, &keys); err != nil {
		e.skip(fmt.Sprintf("%s/%s: blob keys", sliceName, bucket), err)
		return
	}
	for _, k := range keys {
		raw, err := sliceGet(sliceName,
			fmt.Sprintf("%s/ops/backbone/blob/get?bucket=%s&key=%s",
				common.APIBaseURL, url.QueryEscape(bucket), url.QueryEscape(k)),
			"export blob")
		if err != nil {
			e.skip(fmt.Sprintf("%s/%s/%s", sliceName, bucket, k), err)
			continue
		}
		// The key verbatim, so a key containing slashes lands as the nested path
		// it already is rather than as a filename with slashes in it.
		e.add(path.Join(base, "blobs", bucket, k), raw)
	}
}

// exportSecrets writes the NAMES, and the values only when asked.
func exportSecrets(e *exportWriter, sliceName, base string, includeValues bool) {
	body, err := sliceGet(sliceName, common.APIBaseURL+"/ops/backbone/secret/list", "export secret names")
	if err != nil {
		e.skip(fmt.Sprintf("%s: secret names", sliceName), err)
		return
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		e.skip(fmt.Sprintf("%s: secret names", sliceName), err)
		return
	}
	if !includeValues {
		e.addJSON(path.Join(base, "secrets.json"), map[string]any{
			"names": names,
			"note":  "Values are NOT in this archive. Re-run with --include-secret-values to include them.",
		})
		return
	}
	values := map[string]string{}
	for _, n := range names {
		raw, err := sliceGet(sliceName,
			fmt.Sprintf("%s/ops/backbone/secret/get?name=%s", common.APIBaseURL, url.QueryEscape(n)),
			"export secret")
		if err != nil {
			e.skip(fmt.Sprintf("%s: secret %s", sliceName, n), err)
			continue
		}
		// `secret/get` answers RAW, which is why the CLI prints its body
		// verbatim — so this is the value itself and not a JSON envelope.
		values[n] = string(raw)
	}
	e.addJSON(path.Join(base, "secrets.json"), map[string]any{
		"names":  names,
		"values": values,
		"note":   "CONTAINS PLAINTEXT SECRETS. Treat this archive as a credential.",
	})
}

// exportDeed writes the identity pillar's records: vault entries, device
// registries, and pocket key names.
//
// Pocket VALUES are absent and cannot be otherwise — they are encrypted
// client-side and the platform holds no key for them, so there is nothing to
// export but the names. Vault blobs ARE included: they are the tenant's own
// ciphertext, which is exactly the thing an export exists to hand back.
func exportDeed(e *exportWriter, sliceName, base string) {
	uids := deedList(e, sliceName, "/ops/deed/admin/vault/list", fmt.Sprintf("%s: vault uids", sliceName))
	if len(uids) > 0 {
		vault := map[string]json.RawMessage{}
		for _, uid := range uids {
			raw, err := sliceGet(sliceName,
				fmt.Sprintf("%s/ops/deed/admin/vault/get?uid=%s", common.APIBaseURL, url.QueryEscape(uid)),
				"export vault entry")
			if err != nil {
				e.skip(fmt.Sprintf("%s: vault %s", sliceName, uid), err)
				continue
			}
			vault[uid] = raw
		}
		e.addJSON(path.Join(base, "deed", "vault.json"), vault)
	}

	identities := deedList(e, sliceName, "/ops/deed/admin/link/list", fmt.Sprintf("%s: link identities", sliceName))
	if len(identities) > 0 {
		devices := map[string]json.RawMessage{}
		pocket := map[string]json.RawMessage{}
		for _, id := range identities {
			if raw, err := sliceGet(sliceName,
				fmt.Sprintf("%s/ops/deed/admin/link/get?identity=%s", common.APIBaseURL, url.QueryEscape(id)),
				"export device registry"); err == nil {
				devices[id] = raw
			} else {
				e.skip(fmt.Sprintf("%s: devices for %s", sliceName, id), err)
			}
			if raw, err := sliceGet(sliceName,
				fmt.Sprintf("%s/ops/deed/admin/pocket/list?identity=%s", common.APIBaseURL, url.QueryEscape(id)),
				"export pocket keys"); err == nil {
				pocket[id] = raw
			} else {
				e.skip(fmt.Sprintf("%s: pocket keys for %s", sliceName, id), err)
			}
		}
		e.addJSON(path.Join(base, "deed", "link.json"), devices)
		e.addJSON(path.Join(base, "deed", "pocket.json"), map[string]any{
			"keys": pocket,
			"note": "Key names only. Pocket values are encrypted client-side and the platform holds no key for them.",
		})
	}
}

// deedList reads one of Deed's admin listings, treating "this slice has none"
// as an empty list rather than a problem.
func deedList(e *exportWriter, sliceName, route, what string) []string {
	body, err := sliceGet(sliceName, common.APIBaseURL+route, "export deed records")
	if err != nil {
		// A slice with no Deed usage answers an empty list rather than an error,
		// so anything here is worth recording.
		e.skip(what, err)
		return nil
	}
	var out []string
	if err := json.Unmarshal(body, &out); err != nil {
		e.skip(what, err)
		return nil
	}
	return out
}

func orErr(err error, fallback string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s", fallback)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// readme is the first thing a person opens, and it says what is NOT here.
//
// An archive that lists only what it contains invites the reader to assume the
// rest was nothing. The omissions are deliberate and each has a reason, so they
// are written down beside the data rather than left to be inferred.
func readme(username, stamp string, includeSecretValues bool, problems []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Drift account export\n")
	fmt.Fprintf(&b, "account: %s\n", username)
	fmt.Fprintf(&b, "taken:   %s\n\n", stamp)
	b.WriteString("WHAT IS HERE\n")
	b.WriteString("  account.json            the account this was taken from\n")
	b.WriteString("  slices/<slice>/slice.json   the slice's shape: tier, price, quotas,\n")
	b.WriteString("                              declared collections and buckets\n")
	b.WriteString("  slices/<slice>/nosql/       every document in every declared collection\n")
	b.WriteString("  slices/<slice>/blobs/       every object in every declared bucket, as bytes\n")
	b.WriteString("  slices/<slice>/secrets.json the NAMES of this slice's secrets\n")
	b.WriteString("  slices/<slice>/deed/        vault entries, device registries, pocket key names\n\n")
	b.WriteString("WHAT IS DELIBERATELY NOT HERE\n")
	if includeSecretValues {
		b.WriteString("  (secret values ARE included in this archive — treat it as a credential)\n")
	} else {
		b.WriteString("  Secret VALUES. Re-run with --include-secret-values if you need them.\n")
		b.WriteString("  A leaked archive should not be the same thing as a leaked credential store.\n")
	}
	b.WriteString("  Your password, and anything about how the account is protected. An export\n")
	b.WriteString("  is your DATA; the material guarding it is not data you need a copy of.\n")
	b.WriteString("  Pocket VALUES. They are encrypted on your own device and Drift holds no key\n")
	b.WriteString("  for them, so there is nothing here to give you but the key names.\n")
	b.WriteString("  Your deployed function CODE. `drift atomic` deploys it from your own repo;\n")
	b.WriteString("  the source of record is where you wrote it.\n")
	if len(problems) > 0 {
		fmt.Fprintf(&b, "\nINCOMPLETE — %d thing(s) could not be read and are NOT in this archive:\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
		b.WriteString("\nEverything else above was written normally. This archive is not a complete\n")
		b.WriteString("copy of the account until those are resolved and it is taken again.\n")
	}
	return b.String()
}
