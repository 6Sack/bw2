package util

import "strings"

//A URI looks like
// a/b/c/d ..
// it has no slash at the start or end. There may be many plusses, and/or one star
// each cell must look like:
// [!a-zA-Z0-9-_.\(\),]?[a-zA-Z0-9-_.\(\),]+
// or "+", "$", "*"
// Note that a cell starting with an exclamation point denotes the start of a
// metadata tree. It is an error to have more than one exclamation point in
// a URI or for it to occur not at the first character of a cell
// A "$" cell denotes the start of a read-only free-path. It may be accessed
// even if the person does not have permissions for the tree above it, although
// finding it in that case can be difficult

//AnalyzeSuffix checks a given URI for schema validity and possession of characteristics
func AnalyzeSuffix(uri string) (valid, hasStar, hasPlus, hasDollar, hasBang bool) {
	cells := strings.Split(uri, "/")
	valid = false
	hasStar = false
	hasPlus = false
	hasDollar = false
	hasBang = false

	for _, c := range cells {
		ln := len(c)
		switch ln {
		case 0:
			return
		case 1:
			switch c {
			case "*":
				if hasStar {
					return
				}
				hasStar = true
			case "+":
				hasPlus = true
			case "!":
				return
			case "$":
				hasDollar = true
			default:
				k := c[0]
				if !('0' <= k && k <= '9' ||
					'a' <= k && k <= 'z' ||
					'A' <= k && k <= 'Z' ||
					k == '-' || k == '_' ||
					k == ',' || k == '(' ||
					k == ')' || k == '.') {
					return
				}
			}
		default:
			if c[0] == '!' {
				if hasBang {
					return
				}
				hasBang = true
				c = c[1:]
			}
			for i := 0; i < len(c); i++ {
				k := c[i]
				if !('0' <= k && k <= '9' ||
					'a' <= k && k <= 'z' ||
					'A' <= k && k <= 'Z' ||
					k == '-' || k == '_' ||
					k == ',' || k == '(' ||
					k == ')' || k == '.') {
					return
				}
			}
		}
	}
	valid = true
	return
}

func VerifyMVK(mvk []byte) bool {
	return len(mvk) == 32
}

// RestrictBy takes a topic, and a permission, and returns the intersection
// that represents the from topic restricted by the permission. It took a
// looong time to work out this logic...
func RestrictBy(from string, by string) (string, bool) {
	fp := strings.Split(from, "/")
	bp := strings.Split(by, "/")
	fout := make([]string, 0, len(fp)+len(bp))
	bout := make([]string, 0, len(fp)+len(bp))
	var fsx, bsx int
	for fsx = 0; fsx < len(fp) && fp[fsx] != "*"; fsx++ {
	}
	for bsx = 0; bsx < len(bp) && bp[bsx] != "*"; bsx++ {
	}
	fi, bi := 0, 0
	fni, bni := len(fp)-1, len(bp)-1
	emit := func() (string, bool) {
		for i := 0; i < len(bout); i++ {
			fout = append(fout, bout[len(bout)-i-1])
		}
		return strings.Join(fout, "/"), true
	}
	//phase 1
	//emit matching prefix
	for ; fi < len(fp) && bi < len(bp); fi, bi = fi+1, bi+1 {
		if fp[fi] == bp[bi] || (bp[bi] == "+" && fp[fi] != "*") {
			fout = append(fout, fp[fi])
		} else if fp[fi] == "+" && bp[bi] != "*" {
			fout = append(fout, bp[bi])
		} else {
			break
		}
	}
	//phase 2
	//emit matching suffix
	for ; fni >= fi && bni >= bi; fni, bni = fni-1, bni-1 {
		if fp[fni] == bp[bni] || (bp[bni] == "+" && fp[fni] != "*") {
			bout = append(bout, fp[fni])
		} else if fp[fni] == "+" && bp[bni] != "*" {
			bout = append(bout, bp[bni])
		} else {
			break
		}
	}
	//phase 3
	//emit front
	if fi < len(fp) && fp[fi] == "*" {
		for ; bi < len(bp) && bp[bi] != "*" && bi <= bni; bi++ {
			fout = append(fout, bp[bi])
		}
	} else if bi < len(bp) && bp[bi] == "*" {
		for ; fi < len(fp) && fp[fi] != "*" && fi <= fni; fi++ {
			fout = append(fout, fp[fi])
		}
	}
	//phase 4
	//emit back
	if fni >= 0 && fp[fni] == "*" {
		for ; bni >= 0 && bp[bni] != "*" && bni >= bi; bni-- {
			bout = append(bout, bp[bni])
		}
	} else if bni >= 0 && bp[bni] == "*" {
		for ; fni >= 0 && fp[fni] != "*" && fni >= fi; fni-- {
			bout = append(bout, fp[fni])
		}
	}
	//phase 5
	//emit star if they both have it
	if fi == fni && fp[fi] == "*" && bi == bni && bp[bi] == "*" {
		fout = append(fout, "*")
		return emit()
	}
	//Remove any stars
	if fi < len(fp) && fp[fi] == "*" {
		fi++
	}
	if bi < len(bp) && bp[bi] == "*" {
		bi++
	}
	if (fi == fni+1 || fi == len(fp)) && (bi == bni+1 || bi == len(bp)) {
		return emit()
	}
	return "", false
}
