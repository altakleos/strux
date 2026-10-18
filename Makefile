# make check runs the checks in .github/workflows/ci.yml that one machine can run, with the same
# commands and tool versions. CI also audits the workflows with zizmor, runs the tests on Windows,
# compares a macOS build of the release with the Linux build, and runs each platform's archive on
# that platform.
#
# Each tool is pinned in its own module under tools/, so it builds with the dependencies its
# authors tested, and none of them reach strux's module. make workflows also needs shellcheck,
# which actionlint runs on the workflows' scripts; CI's runners have it installed.

tool = go tool -modfile=tools/$(1)/go.mod $(1)
tool_modules := $(patsubst %/go.mod,%,$(wildcard tools/*/go.mod))

# A go.work file in a parent directory would put go in workspace mode, which rejects -modfile.
export GOWORK := off

.PHONY: check tidy lint test vuln workflows release-check

check: tidy lint test vuln workflows release-check

# go.mod and go.sum are tidy, and every downloaded module matches go.sum.
tidy:
	go mod tidy -diff
	go mod verify
	@for m in $(tool_modules); do go -C $$m mod tidy -diff && go -C $$m mod verify || exit 1; done

lint:
	$(call tool,golangci-lint) run

test:
	go test -race -shuffle=on ./...

vuln:
	$(call tool,govulncheck) ./...

workflows:
	@command -v shellcheck >/dev/null || { echo "make workflows needs shellcheck, which actionlint runs on the workflows' scripts"; exit 1; }
	$(call tool,actionlint)

# Builds the whole release, as a tag would but with a snapshot version, without publishing it,
# into dist/.
release-check:
	$(call tool,goreleaser) check
	$(call tool,goreleaser) release --snapshot --clean
