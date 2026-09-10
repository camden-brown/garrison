package comp

// DiffOp is what happened to a line.
type DiffOp uint8

const (
	DiffKeep DiffOp = iota
	DiffRemove
	DiffAdd
)

// DiffLine is one line of a rendered difference.
type DiffLine struct {
	Op   DiffOp
	Text string
}

// DiffLines is the line-level difference between two versions of a file.
//
// A real longest-common-subsequence diff rather than a line-by-line
// comparison, because the cheap version is wrong in the case that matters: a
// setting inserted near the top of a config file shifts every line below it,
// and a positional compare would report the whole file as changed. An operator
// looking at "47 lines changed" before applying learns nothing and stops
// reading the diff at all.
//
// The implementation is the textbook dynamic-programming LCS. Config files are
// tens of lines, so the quadratic table costs nothing and the alternative —
// a linear-space variant nobody can read — buys nothing here.
func DiffLines(before, after []string) []DiffLine {
	n, m := len(before), len(after)

	// lengths[i][j] is the LCS length of before[i:] and after[j:].
	lengths := make([][]int, n+1)
	for i := range lengths {
		lengths[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if before[i] == after[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
				continue
			}
			if lengths[i+1][j] >= lengths[i][j+1] {
				lengths[i][j] = lengths[i+1][j]
			} else {
				lengths[i][j] = lengths[i][j+1]
			}
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case before[i] == after[j]:
			out = append(out, DiffLine{Op: DiffKeep, Text: before[i]})
			i, j = i+1, j+1
		case lengths[i+1][j] >= lengths[i][j+1]:
			out = append(out, DiffLine{Op: DiffRemove, Text: before[i]})
			i++
		default:
			out = append(out, DiffLine{Op: DiffAdd, Text: after[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{Op: DiffRemove, Text: before[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{Op: DiffAdd, Text: after[j]})
	}
	return out
}

// DiffChanged reports whether anything actually differs, which is what decides
// between showing a diff and saying there is nothing to show.
func DiffChanged(lines []DiffLine) bool {
	for _, l := range lines {
		if l.Op != DiffKeep {
			return true
		}
	}
	return false
}

// DiffContext drops runs of unchanged lines longer than n, leaving n lines of
// context either side of every change.
//
// A config file is mostly unchanged on any given apply, and a diff that makes
// you scroll past forty identical lines to find the one that moved is a diff
// that gets skipped. Elided runs come back as a single DiffKeep with empty
// text, which the caller renders as a gap.
func DiffContext(lines []DiffLine, n int) []DiffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == DiffKeep {
			continue
		}
		for j := i - n; j <= i+n; j++ {
			if j >= 0 && j < len(lines) {
				keep[j] = true
			}
		}
	}

	var out []DiffLine
	elided := false
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
			elided = false
			continue
		}
		if !elided {
			out = append(out, DiffLine{Op: DiffKeep})
			elided = true
		}
	}
	return out
}
