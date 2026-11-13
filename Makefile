# Entry points for the release standard. `check` is static only; `test` runs
# the shell and Go tests (git, ssh-keygen and Go required; network for the
# first yaml.v3 download).
.PHONY: check test

check:
	for f in scripts/*.sh; do sh -n "$$f"; done
	for f in tests/*.sh; do bash -n "$$f"; done
	shellcheck -x scripts/*.sh tests/*.sh
	actionlint .github/workflows/*.yml template/release.yml
	go build ./...
	go vet ./...

test:
	bash tests/run-all.sh
