#!/usr/bin/env bash
# Runs every release-standard test script and fails if any of them fails.
set -eu -o pipefail
dir=$(cd "$(dirname "$0")" && pwd -P)
failed=
for t in tag-gate-test.sh signing-gate-test.sh lint-test.sh harness-control-test.sh; do
	echo "== $t"
	bash "$dir/$t" || failed="$failed $t"
done
if [ -n "$failed" ]; then
	echo "run-all: FAILED:$failed" >&2
	exit 1
fi
echo 'run-all: OK'
