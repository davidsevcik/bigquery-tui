#!/usr/bin/env bash
# Prints statement coverage per package (lowest first) and in total from a Go
# coverage profile: scripts/coverage.sh coverage.out
#
# A profile from `go test -coverpkg` lists a block once per test binary; a
# statement counts as covered if any test binary ran it.
set -euo pipefail

profile=${1:?usage: scripts/coverage.sh PROFILE}
module=$(go list -m)

awk -v module="$module/" '
NR == 1 { next } # mode: line
{
	block = $1
	if (!(block in stmts)) {
		pkg = block
		sub(/:.*/, "", pkg)       # drop :line.col,line.col
		sub(/\/[^\/]*$/, "", pkg) # drop the file name
		sub("^" module, "", pkg)
		stmts[block] = $2
		pkgOf[block] = pkg
	}
	if ($3 > 0) hit[block] = 1
}
END {
	for (b in stmts) {
		p = pkgOf[b]
		total[p] += stmts[b]
		all += stmts[b]
		if (b in hit) { covered[p] += stmts[b]; allCovered += stmts[b] }
	}
	printf "%6s  %6s  %s\n", "cover", "stmts", "package"
	for (p in total) printf "%5.1f%%  %6d  %s\n", 100 * covered[p] / total[p], total[p], p | "sort -n"
	close("sort -n")
	printf "%5.1f%%  %6d  %s\n", 100 * allCovered / all, all, "total"
}' "$profile"
