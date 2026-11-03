#!/usr/bin/env bash
# Code quality report: lint, tests with coverage, complexity, dead code,
# vulnerabilities and module tidiness, in that order. Run from anywhere in the
# repo; it needs only Go. Every tool runs through `go run` at the version pinned
# below, so nothing is installed and go.mod is untouched.
#
# Exit code: non-zero if lint, tests, the vulnerability scan or the tidiness
# check fails. Coverage, complexity and dead code are reported, never gated.
# Every section runs even when an earlier one fails.
#
# CI (.github/workflows/ci.yml) pins the same golangci-lint and govulncheck
# versions; change them in both places.
set -uo pipefail

GOLANGCI_LINT=github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.9.0
GOCYCLO=github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
DEADCODE=golang.org/x/tools/cmd/deadcode@v0.52.0

cd "$(dirname "$0")/.." || exit 2
export NO_COLOR=1

failed=()
section() { printf '\n== %s ==\n' "$1"; }
check() { # check NAME CMD...: run CMD, remember NAME if it fails
	local name=$1
	shift
	if ! "$@"; then
		failed+=("$name")
	fi
}

section "lint"
check lint go run "$GOLANGCI_LINT" run ./...

section "tests and coverage"
# -coverpkg credits a package for the lines other packages' tests run. The
# per-package lines of passing packages are dropped: with -coverpkg their
# percentages are shares of the whole module, which the table below replaces.
rm -f coverage.out
if go test -race -count=1 -coverprofile=coverage.out -coverpkg=./... ./... 2>&1 |
	awk '!/^(ok |\t).*coverage: /'; then
	echo "all tests passed"
else
	failed+=("tests")
fi
if [[ -s coverage.out ]]; then
	echo
	scripts/coverage.sh coverage.out
fi

section "complexity (top 10 functions by cyclomatic complexity, tests excluded)"
go run "$GOCYCLO" -top 10 -avg -ignore '_test\.go$' cmd internal

section "dead code (unreachable from main packages and tests)"
dead=$(go run "$DEADCODE" -test ./... 2>&1)
if [[ -n $dead ]]; then
	echo "$dead"
else
	echo "none"
fi

section "vulnerabilities"
check vulnerabilities go run "$GOVULNCHECK" ./...

section "module tidiness"
if go mod tidy -diff; then
	echo "go.mod and go.sum are tidy"
else
	failed+=("tidiness")
fi

section "result"
if ((${#failed[@]})); then
	echo "FAILED: ${failed[*]}"
	exit 1
fi
echo "OK"
