package project

import (
	"sort"
	"testing"
)

// The structure of the format is read from the structs the reader decodes into,
// and only the prose is written down. This holds the two together: every block
// and attribute the reader accepts has a description and every attribute a
// type, and every description describes something the reader accepts.
func TestFormat_DescribesEveryBlockAndAttribute(t *testing.T) {
	reached := map[string]bool{}
	var walk func(b FormatBlock)
	walk = func(b FormatBlock) {
		reached[b.Path] = true
		if b.Doc == "" {
			t.Errorf("block %q has no description in formatDocs", b.Path)
		}
		for _, a := range b.Attrs {
			path := a.Name
			if b.Path != "" {
				path = b.Path + "." + a.Name
			}
			reached[path] = true
			if a.Doc == "" || a.Type == "" {
				t.Errorf("attribute %q has no description or no type in formatDocs", path)
			}
		}
		for _, c := range b.Blocks {
			walk(c)
		}
	}
	walk(DriftfileFormat())

	var stale []string
	for path := range formatDocs {
		if !reached[path] {
			stale = append(stale, path)
		}
	}
	sort.Strings(stale)
	for _, path := range stale {
		t.Errorf("formatDocs describes %q, which the reader does not accept", path)
	}
	if len(reached) < 50 {
		t.Errorf("the walk reached %d paths; the format has far more, so the reflection read nothing", len(reached))
	}
}

// What a required attribute is, the compiler refuses without. The control is
// one the compiler leaves optional.
func TestFormat_MarksWhatTheCompilerRequires(t *testing.T) {
	f := DriftfileFormat()
	for path, want := range map[string]bool{
		"atomic.element.pulse.memory":  true,
		"atomic.element.pulse.handler": true,
		"backbone.secret.value":        true,
		"atomic.element.pulse.cron":    false,
		"backbone.queue.depth":         false,
	} {
		_, attr, _ := f.Find(path)
		if attr == nil {
			t.Errorf("%s is not an attribute of the format", path)
			continue
		}
		if attr.Required != want {
			t.Errorf("%s required = %v, want %v", path, attr.Required, want)
		}
	}
}

// Find reaches a block, an attribute and the block it is in, and answers
// nothing for a name the format does not have.
func TestFormat_Find(t *testing.T) {
	f := DriftfileFormat()
	if b, a, _ := f.Find("atomic.element.pulse"); b == nil || a != nil || b.Name != "pulse" || len(b.Labels) != 1 || b.Labels[0] != "trigger" {
		t.Errorf("atomic.element.pulse: block %+v, attr %+v", b, a)
	}
	if b, a, in := f.Find("atomic.element.pulse.memory"); b != nil || a == nil || a.Name != "memory" || in == nil || in.Name != "pulse" {
		t.Errorf("atomic.element.pulse.memory: block %+v, attr %+v, in %+v", b, a, in)
	}
	if b, a, in := f.Find("atomic.element.puls"); b != nil || a != nil || in == nil || in.Name != "element" {
		t.Errorf("atomic.element.puls: block %+v, attr %+v, in %+v; want nothing found, in element", b, a, in)
	}
}
