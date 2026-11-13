# shellcheck shell=bash
# Shared harness for the release-standard shell tests. Source it from bash.
#
# make_scratch creates an absolute, physical scratch root under the repo's
# .temp/ and points HOME, XDG dirs, TMPDIR and the global git config into it,
# so no host configuration (signing keys, agent sockets, git -c environment)
# leaks into a fixture. The scratch is removed on success and kept on failure.

TESTS_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
REPO_ROOT=$(cd "$TESTS_DIR/.." && pwd -P)
FAILURES=0
KEEP_SCRATCH=0
SCRATCH=

make_scratch() {
	mkdir -p "$REPO_ROOT/.temp"
	SCRATCH=$(mktemp -d "$REPO_ROOT/.temp/test-env.XXXXXX")
	SCRATCH=$(cd "$SCRATCH" && pwd -P)
	case $SCRATCH in
	/*) ;;
	*) echo "scratch root is not absolute: $SCRATCH" >&2 && exit 2 ;;
	esac
	mkdir -p "$SCRATCH/home" "$SCRATCH/config" "$SCRATCH/cache" "$SCRATCH/tmp" "$SCRATCH/work"
	trap '[ "$KEEP_SCRATCH" = 1 ] || rm -rf "$SCRATCH"' EXIT
	export HOME="$SCRATCH/home" USERPROFILE="$SCRATCH/home"
	export XDG_CONFIG_HOME="$SCRATCH/config" XDG_CACHE_HOME="$SCRATCH/cache"
	export TMPDIR="$SCRATCH/tmp" TMP="$SCRATCH/tmp" TEMP="$SCRATCH/tmp"
	export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$SCRATCH/home/.gitconfig"
	: >"$GIT_CONFIG_GLOBAL"
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR \
		GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_SSH_COMMAND \
		GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL \
		SSH_AUTH_SOCK GOTELEMETRYDIR
}

pass() {
	printf 'PASS %s\n' "$1"
}

fail() {
	printf 'FAIL %s: %s\n' "$1" "$2" >&2
	FAILURES=$((FAILURES + 1))
}

finish() {
	if [ "$FAILURES" -ne 0 ]; then
		KEEP_SCRATCH=1
		printf '%s: %d failure(s); scratch kept at %s\n' "$1" "$FAILURES" "$SCRATCH" >&2
		exit 1
	fi
	printf '%s: OK\n' "$1"
}
