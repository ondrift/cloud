package slice

import "testing"

// `slice create` is gone: a Driftfile declares the slice and `drift apply`
// creates it, and `drift file new` writes the file.
func TestSlice_CreateIsGone(t *testing.T) {
	for _, c := range GetCmd().Commands() {
		if c.Name() == "create" {
			t.Error("`drift slice create` is still registered")
		}
	}
}

// `shrink` was a deprecated spelling of `resize` and is gone; `resize` is the one
// verb.
func TestSlice_ShrinkIsGone(t *testing.T) {
	for _, c := range GetCmd().Commands() {
		if c.Name() == "shrink" {
			t.Error("`drift slice shrink` is still registered")
		}
	}
}
