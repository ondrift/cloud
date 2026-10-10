package project

// unidiff.go: a unified diff of two versions of one file, in the format
// `git apply` and `patch` read, so what `drift size` shows is a patch and not
// only a picture of one.
//
// A longest-common-subsequence over lines, after the common head and tail are
// set aside: a Driftfile is a few hundred lines and an edit touches a few of
// them, so the table stays small without a cleverer algorithm.

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines frame each change, as in `diff -u`.
const diffContext = 3

type diffOp struct {
	kind byte // ' ', '-' or '+'
	text string
}

// UnifiedDiff renders the changes from a to b, or "" when there are none.
func UnifiedDiff(aName, bName string, a, b []byte) string {
	if string(a) == string(b) {
		return ""
	}
	ops := diffLines(splitLines(string(a)), splitLines(string(b)))

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", aName, bName)

	aLine, bLine := 1, 1
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			aLine++
			bLine++
			i++
			continue
		}
		// A hunk opens diffContext lines before this change and runs until a
		// stretch of more than twice that many unchanged lines.
		start := max(i-diffContext, 0)
		for back := i; back > start; back-- {
			aLine--
			bLine--
		}
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(end+diffContext, len(ops))
				break
			}
			end = run
		}

		aCount, bCount := 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				aCount++
			}
			if op.kind != '-' {
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(aLine, aCount), hunkRange(bLine, bCount))
		for _, op := range ops[start:end] {
			out.WriteByte(op.kind)
			if text, ok := strings.CutSuffix(op.text, "\n"); ok {
				out.WriteString(text + "\n")
			} else {
				out.WriteString(op.text + "\n\\ No newline at end of file\n")
			}
		}
		aLine += aCount
		bLine += bCount
		i = end
	}
	return out.String()
}

// hunkRange is one side of a hunk header. An empty side names the line before
// it, which is how the format says "inserted after line N".
func hunkRange(line, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", line-1)
	}
	if count == 1 {
		return fmt.Sprintf("%d", line)
	}
	return fmt.Sprintf("%d,%d", line, count)
}

// splitLines keeps each line's "\n", so a last line without one differs from
// the same line with one, as it does on disk.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines is the edit script from a to b.
func diffLines(a, b []string) []diffOp {
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	am, bm := a[head:len(a)-tail], b[head:len(b)-tail]

	// lcs[i][j] is the length of the longest common subsequence of am[i:] and
	// bm[j:].
	lcs := make([][]int, len(am)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(bm)+1)
	}
	for i := len(am) - 1; i >= 0; i-- {
		for j := len(bm) - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:head] {
		ops = append(ops, diffOp{' ', l})
	}
	i, j := 0, 0
	for i < len(am) && j < len(bm) {
		switch {
		case am[i] == bm[j]:
			ops = append(ops, diffOp{' ', am[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', am[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', bm[j]})
			j++
		}
	}
	for ; i < len(am); i++ {
		ops = append(ops, diffOp{'-', am[i]})
	}
	for ; j < len(bm); j++ {
		ops = append(ops, diffOp{'+', bm[j]})
	}
	for _, l := range a[len(a)-tail:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}
