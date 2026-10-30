#!/usr/bin/env bash
# Checks run indirectly through step(), which shellcheck cannot follow.
# shellcheck disable=SC2329

# Local validation for go-smpp. The repository runs no hosted CI: run this
# before pushing and before merging. It exits non-zero when any check fails.
#
# Usage: scripts/validate.sh [--fast] [--fuzz DURATION] [--skip-codeql] [--base REF]
#   --fast          skip the extra toolchains, the race detector and CodeQL
#   --fuzz DUR      also run every fuzz target for DUR (for example 30s)
#   --skip-codeql   skip CodeQL even when the CLI is installed
#   --base REF      check whitespace in commits since REF (default origin/main)
#
# Environment:
#   VALIDATE_TOOLCHAINS  extra Go toolchains to test with, separated by spaces
#                        (for example "go1.26.9 go1.27.2"). Defaults to the
#                        releases go.dev lists as supported, when reachable.
#
# Tools are pinned below and built with the current Go toolchain into
# ${XDG_CACHE_HOME:-~/.cache}/go-smpp-validate, so they always understand the
# export data of the toolchain that runs them.
set -euo pipefail

# 2026.2.2 is the first release that reads Go 1.27 export data
# (https://github.com/dominikh/go-tools/issues/1832).
STATICCHECK=honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOLANGCI_LINT=github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.8.0
GITLEAKS=github.com/zricethezav/gitleaks/v8@v8.30.1

# Cross targets: vet and test compilation only; nothing runs on them. They
# cover 64-bit ARM, 32-bit (int is 32 bits), big-endian, Windows and macOS.
CROSS_TARGETS="linux/arm64 linux/386 linux/arm linux/s390x linux/mips windows/amd64 darwin/amd64 darwin/arm64"

fast=0
fuzz_time=""
skip_codeql=0
base_ref="origin/main"
while [ $# -gt 0 ]; do
	case "$1" in
	--fast) fast=1 ;;
	--fuzz)
		fuzz_time="${2:?--fuzz needs a duration}"
		shift
		;;
	--fuzz=*) fuzz_time="${1#--fuzz=}" ;;
	--skip-codeql) skip_codeql=1 ;;
	--base)
		base_ref="${2:?--base needs a ref}"
		shift
		;;
	--base=*) base_ref="${1#--base=}" ;;
	-h | --help)
		sed -n '/^# Local validation/,/^set -euo/p' "$0" | sed '$d'
		exit 0
		;;
	*)
		echo "unknown option: $1" >&2
		exit 2
		;;
	esac
	shift
done

cd "$(git rev-parse --show-toplevel)"
export GOFLAGS="${GOFLAGS:-}"

tools_dir="${XDG_CACHE_HOME:-$HOME/.cache}/go-smpp-validate/$(go env GOVERSION)"

# tool NAME PKG@VERSION prints the path of NAME built at that version.
tool() {
	local name=$1 spec=$2
	local dir="$tools_dir/${spec//[\/@]/_}"
	if [ ! -x "$dir/$name" ]; then
		echo "building $spec" >&2
		GOBIN="$dir" GOFLAGS='' go install "$spec" >&2
	fi
	printf '%s\n' "$dir/$name"
}

results=()
failed=0
# Checks run inside an if, where bash disables errexit: every check below
# propagates failures explicitly instead of relying on set -e.
step() {
	local name=$1
	shift
	printf '\n==> %s\n' "$name"
	local start=$SECONDS
	if "$@"; then
		results+=("ok    $name ($((SECONDS - start))s)")
	else
		results+=("FAIL  $name ($((SECONDS - start))s)")
		failed=1
	fi
}

check_format() {
	local unformatted
	# Tracked and untracked (not ignored) files: the same set go build sees.
	unformatted="$(git ls-files -z --cached --others --exclude-standard -- '*.go' |
		xargs -0 -r gofmt -l)" || return 1
	if [ -n "$unformatted" ]; then
		echo "not gofmt'd:"
		echo "$unformatted"
		return 1
	fi
}

check_whitespace() {
	git diff --check && git diff --cached --check || return 1
	if git rev-parse --verify --quiet "$base_ref" >/dev/null; then
		git diff --check "$base_ref"...HEAD
	else
		echo "base $base_ref not found; checked uncommitted changes only"
	fi
}

check_modules() {
	go mod tidy -diff && go mod verify
}

check_cross() {
	local target goos goarch rc=0 run_nothing
	run_nothing="$(command -v true)"
	for target in $CROSS_TARGETS; do
		goos="${target%/*}" goarch="${target#*/}"
		echo "-- $target"
		GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go vet ./... || rc=1
		GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
			go test ./... -run '^$' -exec "$run_nothing" >/dev/null || rc=1
	done
	return "$rc"
}

# Prints the extra toolchains to test with, or fails when they cannot be
# determined: skipping the matrix silently would hide a coverage gap.
# VALIDATE_TOOLCHAINS=none skips it explicitly.
supported_toolchains() {
	if [ -n "${VALIDATE_TOOLCHAINS:-}" ]; then
		local list
		read -r -a list <<<"$VALIDATE_TOOLCHAINS"
		[ "$VALIDATE_TOOLCHAINS" = none ] || printf '%s\n' "${list[@]}"
		return 0
	fi
	local releases
	releases="$(curl -fsS --max-time 10 'https://go.dev/dl/?mode=json' | jq -r '.[].version')" &&
		[ -n "$releases" ] || {
		echo "cannot list the supported Go releases from go.dev;" \
			"set VALIDATE_TOOLCHAINS (or VALIDATE_TOOLCHAINS=none to skip)" >&2
		return 1
	}
	printf '%s\n' "$releases"
}

check_toolchains() {
	local local_version toolchains tc rc=0 tested=0
	local_version="$(go env GOVERSION)"
	toolchains="$(supported_toolchains)" || return 1
	for tc in $toolchains; do
		[ "$tc" = "$local_version" ] && continue
		tested=1
		echo "-- $tc"
		GOTOOLCHAIN="$tc" go vet ./... || rc=1
		GOTOOLCHAIN="$tc" go test ./... -count=1 || rc=1
	done
	[ "$tested" -eq 1 ] || echo "no other toolchain to test"
	return "$rc"
}

check_staticcheck() {
	local bin
	bin="$(tool staticcheck "$STATICCHECK")" || return 1
	"$bin" ./...
}
check_golangci() {
	local bin
	bin="$(tool golangci-lint "$GOLANGCI_LINT")" || return 1
	"$bin" run --allow-parallel-runners ./...
}
check_vulns() {
	local bin
	bin="$(tool govulncheck "$GOVULNCHECK")" || return 1
	"$bin" ./...
}

# History and working tree: committed secrets and those in the files being
# validated (staged, unstaged or untracked) both fail the check.
check_secrets() {
	local gitleaks rc=0
	gitleaks="$(tool gitleaks "$GITLEAKS")" || return 1
	"$gitleaks" git --redact --no-banner --exit-code 1 . || rc=1
	"$gitleaks" dir --redact --no-banner --exit-code 1 . || rc=1
	return "$rc"
}

# CodeQL runs the suites the hosted scan used. A finding that was reviewed and
# accepted is listed in scripts/codeql-accepted.txt as
# "<rule id> <path> <primaryLocationLineHash>": the hash identifies the result
# by the content of its line and its context, so it survives line shifts but
# not a change to the code it points at. Unaccepted findings are printed in
# that format, ready to paste once reviewed.
check_codeql() {
	local tmp sarif findings accepted rc=0 rule location hash message
	tmp="$(mktemp -d)"
	sarif="$tmp/codeql.sarif"
	findings="$tmp/findings.txt"
	accepted="scripts/codeql-accepted.txt"
	if ! codeql database create "$tmp/db" --language=go --source-root=. \
		--command='go build ./...' --overwrite --quiet ||
		! codeql database analyze "$tmp/db" \
			codeql/go-queries:codeql-suites/go-security-extended.qls \
			codeql/go-queries:codeql-suites/go-security-and-quality.qls \
			--format=sarif-latest --output="$sarif" --quiet ||
		! jq -r '.runs[].results[] |
			"\(.ruleId) \(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine) \(.partialFingerprints.primaryLocationLineHash // "-") \(.message.text)"' \
			"$sarif" >"$findings"; then
		rm -rf "$tmp"
		return 1
	fi
	while read -r rule location hash message; do
		if grep -v '^#' "$accepted" | grep -q -x -F "$rule ${location%%:*} $hash"; then
			echo "accepted: $rule $location $hash"
		else
			echo "$rule ${location%%:*} $hash  # $location: $message"
			rc=1
		fi
	done <"$findings"
	rm -rf "$tmp"
	return "$rc"
}

check_fuzz() {
	local pkg pkgs targets target rc=0
	pkgs="$(go list ./...)" || return 1
	for pkg in $pkgs; do
		targets="$(go test -list '^Fuzz' "$pkg")" || return 1
		for target in $(printf '%s\n' "$targets" | grep '^Fuzz' || true); do
			echo "-- $pkg $target"
			go test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzz_time" \
				-fuzzminimizetime=1s || rc=1
		done
	done
	return "$rc"
}

echo "go: $(go version)"
step "gofmt" check_format
step "whitespace" check_whitespace
step "go.mod tidy and verify" check_modules
step "build" go build ./...
step "vet" go vet ./...
step "cross vet and test compile ($CROSS_TARGETS)" check_cross
step "test" go test ./... -count=1
if [ $fast -eq 0 ]; then
	step "test -race" go test -race ./... -count=1
	step "other supported toolchains" check_toolchains
fi
step "staticcheck" check_staticcheck
step "golangci-lint" check_golangci
step "govulncheck" check_vulns
step "gitleaks (history and working tree)" check_secrets
if [ $fast -eq 0 ] && [ $skip_codeql -eq 0 ]; then
	if command -v codeql >/dev/null; then
		step "CodeQL security-extended + security-and-quality" check_codeql
	else
		results+=("skip  CodeQL (codeql CLI not installed)")
	fi
fi
if command -v shellcheck >/dev/null; then
	step "shellcheck scripts" shellcheck -x scripts/*.sh
else
	results+=("skip  shellcheck (not installed)")
fi
if [ -n "$fuzz_time" ]; then
	step "fuzz ($fuzz_time per target)" check_fuzz
fi

printf '\n%s\n' "Summary:"
printf '  %s\n' "${results[@]}"
exit $failed
