package common

// The registry, errorcodes.json, owns the failure codes. cloud/sites holds a copy of
// it, docs/src/errorcodes.json, and generates its troubleshooting pages from the copy,
// so the copy has to be the registry byte for byte. docsCopyDrift compares the two and
// says which codes differ and how: added, removed, reworded or retired.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// docsCopyDrift compares the registry with the copy of it that cloud/sites holds and
// returns one line for each way the copy differs, in code order. A code only the
// registry has is added, a code only the copy has is removed, a code whose meaning,
// remedy or command differs is reworded, and a code that only one of the two marks
// retired is retired. Identical bytes name nothing.
//
// Bytes that differ with no code differing are one line that names no code: the copy
// is not the registry, and only its layout or its comment can be what differs. A
// side that is not a registry in JSON is one line about that side, and nothing is
// compared then.
func docsCopyDrift(registry, docs []byte) []string {
	// Equal bytes are decided before anything is parsed, so the common case costs
	// nothing and a copy that is only laid out another way cannot pass for equal.
	if bytes.Equal(registry, docs) {
		return nil
	}

	registered, registryErr := docsCopyEntries(registry)
	copied, copyErr := docsCopyEntries(docs)
	var lines []string
	if registryErr != nil {
		lines = append(lines, "the registry is not a registry in JSON: "+registryErr.Error())
	}
	if copyErr != nil {
		lines = append(lines, "the copy is not a registry in JSON: "+copyErr.Error())
	}
	if lines != nil {
		return lines
	}

	// Every code either side holds, once, in code order.
	codes := make([]string, 0, len(registered)+len(copied))
	for code := range registered {
		codes = append(codes, code)
	}
	for code := range copied {
		if _, held := registered[code]; !held {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)

	for _, code := range codes {
		inRegistry, registryHas := registered[code]
		inCopy, copyHas := copied[code]
		switch {
		case !copyHas:
			lines = append(lines, code+" added: in the registry, not in the copy")
		case !registryHas:
			lines = append(lines, code+" removed: in the copy, not in the registry")
		default:
			if inRegistry.Retired != inCopy.Retired {
				marks := "registry"
				if inCopy.Retired {
					marks = "copy"
				}
				lines = append(lines, code+" retired: only the "+marks+" marks it retired")
			}
			if fields := docsCopyWordedFields(inRegistry, inCopy); len(fields) > 0 {
				lines = append(lines, code+" reworded ("+strings.Join(fields, ", ")+")")
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "the copy is not byte-identical to the registry, though no code differs: the two differ only in layout or comment")
	}
	return lines
}

// docsCopyEntries reads the entries of a registry by code. A code listed twice keeps
// its last entry, as the registry's own loader does.
func docsCopyEntries(data []byte) (map[string]ErrorCode, error) {
	var doc struct {
		Codes []ErrorCode `json:"codes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	byCode := make(map[string]ErrorCode, len(doc.Codes))
	for _, entry := range doc.Codes {
		byCode[entry.Code] = entry
	}
	return byCode, nil
}

// docsCopyWordedFields names, as the registry spells them, the fields in which two
// entries of one code differ, leaving out the code and whether it is retired, which
// are reported on their own. It reads the fields of ErrorCode itself, so a field the
// registry gains is compared without this function being touched.
func docsCopyWordedFields(registry, docs ErrorCode) []string {
	var names []string
	r, d := reflect.ValueOf(registry), reflect.ValueOf(docs)
	for i := 0; i < r.NumField(); i++ {
		name, _, _ := strings.Cut(r.Type().Field(i).Tag.Get("json"), ",")
		if name == "code" || name == "retired" {
			continue
		}
		if !reflect.DeepEqual(r.Field(i).Interface(), d.Field(i).Interface()) {
			names = append(names, name)
		}
	}
	return names
}

// docsCopyCodes is the registry the cases below start from: three live codes, the
// last with no command, which the registry allows.
var docsCopyCodes = []ErrorCode{
	{Code: "DRIFT-1001", Meaning: "Your session has expired.", Remedy: "Run `drift account login` and try again.", Command: "drift account login"},
	{Code: "DRIFT-1002", Meaning: "The platform refused the action.", Remedy: "The account lacks the permission for this operation.", Command: "drift slice list"},
	{Code: "DRIFT-1003", Meaning: "The platform failed while handling the request.", Remedy: "Retry once.", Command: ""},
}

// docsCopyRender lays codes out as errorcodes.json is laid out: a comment, then the
// codes, indented two spaces, with a newline at the end.
func docsCopyRender(t *testing.T, codes []ErrorCode) []byte {
	t.Helper()
	out, err := json.MarshalIndent(struct {
		Comment string      `json:"$comment"`
		Codes   []ErrorCode `json:"codes"`
	}{"A fixture.", codes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// docsCopyEdit returns codes with the entry of one code changed by edit. The slice it
// is given is left as it was.
func docsCopyEdit(codes []ErrorCode, code string, edit func(*ErrorCode)) []ErrorCode {
	out := slices.Clone(codes)
	for i := range out {
		if out[i].Code == code {
			edit(&out[i])
		}
	}
	return out
}

// docsCopyDrop returns codes without the entry of one code.
func docsCopyDrop(codes []ErrorCode, code string) []ErrorCode {
	return slices.DeleteFunc(slices.Clone(codes), func(c ErrorCode) bool { return c.Code == code })
}

// docsCopyReversed returns codes in the opposite order.
func docsCopyReversed(codes []ErrorCode) []ErrorCode {
	out := slices.Clone(codes)
	slices.Reverse(out)
	return out
}

// docsCopyShow lays lines out for a failure message, one to a line.
func docsCopyShow(lines []string) string {
	if len(lines) == 0 {
		return "  (no line)"
	}
	return "  " + strings.Join(lines, "\n  ")
}

// The common case costs nothing and is the one that must name nothing: a copy that
// is the registry as it stands.
func TestDocsCopyDrift_IdenticalBytesNameNothing(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	if got := docsCopyDrift(registry, slices.Clone(registry)); len(got) != 0 {
		t.Errorf("a copy that is the registry byte for byte must name nothing, got: %q", got)
	}
	if got := docsCopyDrift(errorCodesJSON, slices.Clone(errorCodesJSON)); len(got) != 0 {
		t.Errorf("the registry the CLI ships, copied byte for byte, must name nothing, got: %q", got)
	}
}

// Each way a code can differ gives a line that starts with the code, and a code that
// does not differ is named nowhere.
func TestDocsCopyDrift_EachWayACopyDiffersNamesItsCode(t *testing.T) {
	cases := []struct {
		name     string
		registry []ErrorCode
		docs     []ErrorCode
		want     []string
	}{
		{
			name:     "a code only the registry has is added",
			registry: docsCopyCodes,
			docs:     docsCopyDrop(docsCopyCodes, "DRIFT-1003"),
			want:     []string{"DRIFT-1003 added: in the registry, not in the copy"},
		},
		{
			name:     "a code only the copy has is removed",
			registry: docsCopyDrop(docsCopyCodes, "DRIFT-1003"),
			docs:     docsCopyCodes,
			want:     []string{"DRIFT-1003 removed: in the copy, not in the registry"},
		},
		{
			name:     "a reworded meaning",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Meaning = "The platform refused it." }),
			want:     []string{"DRIFT-1002 reworded (meaning)"},
		},
		{
			name:     "a reworded remedy",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Remedy = "Ask the owner of the slice." }),
			want:     []string{"DRIFT-1002 reworded (remedy)"},
		},
		{
			name:     "a reworded command",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Command = "drift account login" }),
			want:     []string{"DRIFT-1002 reworded (command)"},
		},
		{
			name:     "a reworded meaning and remedy are one line",
			registry: docsCopyCodes,
			docs: docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) {
				c.Meaning = "The platform refused it."
				c.Remedy = "Ask the owner of the slice."
			}),
			want: []string{"DRIFT-1002 reworded (meaning, remedy)"},
		},
		{
			name:     "a code retired in the registry and live in the copy",
			registry: docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Retired = true }),
			docs:     docsCopyCodes,
			want:     []string{"DRIFT-1002 retired: only the registry marks it retired"},
		},
		{
			name:     "a code live in the registry and retired in the copy",
			registry: docsCopyCodes,
			docs:     docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Retired = true }),
			want:     []string{"DRIFT-1002 retired: only the copy marks it retired"},
		},
		{
			name: "a code retired and reworded gets a line for each",
			registry: docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) {
				c.Retired = true
				c.Remedy = "Nothing to do."
			}),
			docs: docsCopyCodes,
			want: []string{
				"DRIFT-1002 retired: only the registry marks it retired",
				"DRIFT-1002 reworded (remedy)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := docsCopyDrift(docsCopyRender(t, tc.registry), docsCopyRender(t, tc.docs))
			if !slices.Equal(got, tc.want) {
				t.Errorf("got:\n%s\nwant:\n%s", docsCopyShow(got), docsCopyShow(tc.want))
			}
		})
	}
}

// Bytes that differ with no code differing are still a difference, and the copy is
// not the registry: one line, naming no code, because there is none to name. The
// sync script writes the registry's bytes, so a copy laid out another way was not
// made by it.
func TestDocsCopyDrift_ACopyDifferingOnlyInLayoutIsNotIdenticalAndNamesNoCode(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cases := []struct {
		name string
		docs []byte
	}{
		{"indented with four spaces", bytes.ReplaceAll(registry, []byte("\n  "), []byte("\n    "))},
		{"windows line endings", bytes.ReplaceAll(registry, []byte("\n"), []byte("\r\n"))},
		{"no newline at the end", bytes.TrimRight(registry, "\n")},
		{"one space at the end of a line", bytes.Replace(registry, []byte("\n"), []byte(" \n"), 1)},
		{"the comment worded differently", bytes.Replace(registry, []byte("A fixture."), []byte("Another fixture."), 1)},
		{"the entries in another order", docsCopyRender(t, docsCopyReversed(docsCopyCodes))},
		{"a field written that says what leaving it out says", bytes.Replace(registry, []byte(`"command": ""`), []byte(`"command": "", "retired": false`), 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Equal(registry, tc.docs) {
				t.Fatal("the case does not differ from the registry, so it proves nothing")
			}
			got := docsCopyDrift(registry, tc.docs)
			if len(got) != 1 {
				t.Fatalf("want one line, got %d:\n%s", len(got), docsCopyShow(got))
			}
			if strings.Contains(got[0], "DRIFT-") {
				t.Errorf("no code differs, so none may be named, got: %s", got[0])
			}
			if !strings.Contains(got[0], "not byte-identical") {
				t.Errorf("the line must say the copy is not byte-identical to the registry, got: %s", got[0])
			}
		})
	}
}

// A copy that cannot be read as a registry is one line that says so, and the line
// is about the side that is wrong.
func TestDocsCopyDrift_ACopyThatIsNotJSONIsOneLineSayingSo(t *testing.T) {
	registry := docsCopyRender(t, docsCopyCodes)
	cases := []struct {
		name string
		docs []byte
	}{
		{"text", []byte("this is not a registry")},
		{"nothing", nil},
		{"the registry cut in half", registry[:len(registry)/2]},
		{"a JSON array", []byte("[]")},
		{"two documents", slices.Concat(registry, registry)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := docsCopyDrift(registry, tc.docs)
			if len(got) != 1 {
				t.Fatalf("want one line, got %d:\n%s", len(got), docsCopyShow(got))
			}
			if !strings.HasPrefix(got[0], "the copy is not a registry in JSON") {
				t.Errorf("the line must say the copy is not a registry in JSON, got: %s", got[0])
			}
		})
	}

	t.Run("the registry is the one that is not JSON", func(t *testing.T) {
		got := docsCopyDrift([]byte("{"), registry)
		if len(got) != 1 || !strings.HasPrefix(got[0], "the registry is not a registry in JSON") {
			t.Errorf("want one line about the registry, got: %q", got)
		}
	})

	t.Run("neither is JSON", func(t *testing.T) {
		got := docsCopyDrift([]byte("{"), []byte("}"))
		if len(got) != 2 ||
			!strings.HasPrefix(got[0], "the registry is not a registry in JSON") ||
			!strings.HasPrefix(got[1], "the copy is not a registry in JSON") {
			t.Errorf("want a line for the registry and then one for the copy, got: %q", got)
		}
	})
}

// The lines come in code order, however the two files list their entries, so the
// first line names the lowest code that differs.
func TestDocsCopyDrift_NamesCodesInCodeOrderWhateverOrderTheFilesListThem(t *testing.T) {
	// The registry lists 1003 first. The copy lists 1004, 1003 and 1002 in that order,
	// has 1003 retired and 1002 reworded, lacks 1001, and holds a 1004 the registry
	// does not.
	retired := docsCopyEdit(docsCopyCodes, "DRIFT-1003", func(c *ErrorCode) { c.Retired = true })
	reworded := docsCopyEdit(docsCopyCodes, "DRIFT-1002", func(c *ErrorCode) { c.Remedy = "Ask the owner of the slice." })
	registry := []ErrorCode{docsCopyCodes[2], docsCopyCodes[0], docsCopyCodes[1]}
	docs := []ErrorCode{
		{Code: "DRIFT-1004", Meaning: "Only the copy has it.", Remedy: "None."},
		retired[2],
		reworded[1],
	}
	want := []string{
		"DRIFT-1001 added: in the registry, not in the copy",
		"DRIFT-1002 reworded (remedy)",
		"DRIFT-1003 retired: only the copy marks it retired",
		"DRIFT-1004 removed: in the copy, not in the registry",
	}
	got := docsCopyDrift(docsCopyRender(t, registry), docsCopyRender(t, docs))
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", docsCopyShow(got), docsCopyShow(want))
	}
}
