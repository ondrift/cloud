package main

import "testing"

// `drift size` sits beside `drift plan` at the top level, in the same group,
// because it is about the whole file and the slice it declares.
func TestRoot_SizeSitsBesidePlan(t *testing.T) {
	root := newRootCmd("test")
	var plan, size string
	for _, c := range root.Commands() {
		switch c.Name() {
		case "plan":
			plan = c.GroupID
		case "size":
			size = c.GroupID
		}
	}
	if size == "" {
		t.Fatal("drift size is not registered on the root command")
	}
	if size != plan {
		t.Errorf("drift size is in group %q and drift plan in %q", size, plan)
	}
}
