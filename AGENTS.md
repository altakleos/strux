# Building Strux

This repository is the Strux implementation, and it is public. Never refer to a private repository from it, in files, commit messages, pull requests or issues: not by name, link or path, and not by citing its documents or their identifiers.

## Every change is deployable

- Work on a branch and open a pull request. `main` accepts only pull requests whose `deployable` check passed on the tree that lands, merged by squash, so every commit on `main` is a deployable release candidate.
- A pull request's title is its line in the release notes and the subject of its commit on `main`, and its description is that commit's body. Write the title for someone using strux, as an imperative sentence with no trailing period, ideally 50 characters or fewer and never more than 72, such as "Add the CLI skeleton and release pipeline". Label a pull request `breaking` when it changes what an existing use of strux does; `.github/release.yml` groups the notes and lists those first.
- Run `make check` before you push. It runs the checks in `.github/workflows/ci.yml` that one machine can, with the same tool versions: `tidy`, `lint`, `test`, `vuln`, `workflows` and `release-check`, each also a make target. It needs shellcheck, which actionlint runs on the workflows' scripts. CI also audits the workflows with zizmor, runs the tests on Windows, rebuilds the release on macOS and compares the bytes, and runs each platform's archive on that platform.
- A release is a `vX.Y.Z` tag on a commit on `main`; the release workflow ignores any other tag. `.github/workflows/release.yml` checks that `go build` stamps the tag as strux's version, runs `ci.yml` again on that commit, drafts the release with build provenance, runs the drafted archives on their platforms, and publishes the draft only if its assets are exactly the ones checked. Releases are immutable, a repository setting, so a release is made only by pushing its tag. The provenance names `release.yml` as its signer, so keep it to releasing and change it only when the way strux is released changes. Versions are `v0.0.x` until Strux's first release, which is `v0.1.0`; from `v2`, the module path must end in the major version.
- The platforms are the builds in `.goreleaser.yaml`. `internal/cmd/check-release` names the GitHub runner for each, and CI runs every archive built on its runner, so a platform with no runner fails the build.
- Each archive carries `THIRD_PARTY_NOTICES`, which `internal/cmd/notices` writes from the binary the archive holds: the license and notice files of the Go standard library and of every module the binary was built with. A module with no license file fails the build.

## The command line

- A command returns its result and never writes output; `internal/cli` writes every result as YAML, or as JSON with `--json`, from the same value, so both carry the same fields. Help is a result too, generated from the command tree.
- Results go to stdout and diagnostics to stderr. The exit status is 0 on success, 1 on failure and 2 for an invalid command line, and a usage error names the help that explains it.
- The binary's version and commit come from git through `go build`. Never set them with `-ldflags`. `internal/version` reads them, for `strux version` and for check-release.
- Builds are reproducible: keep `-trimpath`, `CGO_ENABLED=0`, no build time in the binary, and the fixed archive metadata in `.goreleaser.yaml`. CI's `reproducible` job fails otherwise.

## Tests

- `cmd/strux/testdata/script/*.txtar` are transcripts of strux as an agent sees it, run by testscript. Add one for each command and each output an agent relies on.
- Unit tests sit beside the code they test; the exit codes are tested in `internal/cli`.

## Dependencies and tools

- The Go toolchain is pinned once, by the `toolchain` line in `go.mod`, and CI installs that version. Dependabot does not update it; govulncheck fails when the pinned toolchain has a known vulnerability, and that is the time to move it to the latest patch release.
- Each tool is pinned in its own module under `tools/` and run as `go tool -modfile=tools/<name>/go.mod <name>`, so it builds with the dependencies its authors tested and none of them reach this module. The `go` line of a tool's module must not be newer than the toolchain, or `go tool` refuses to run it. Add a tool the same way, with its own entry in `.github/dependabot.yml` that allows the tool by name: Dependabot skips the indirect requirements a tool's module lists unless a rule allows them.
- Actions from other repositories are pinned by commit hash, with the version in a comment; workflows and actions in this repository are referenced as `$/path`, which GitHub resolves at the running commit. actionlint is the maintained fork `actionlint.kjanat.dev`, because the original stopped before `$/` existed. zizmor runs through its action, at the version named in `ci.yml`, which Dependabot does not update.
- Dependabot proposes updates to modules, tools and actions once a version is 14 days old.
- A run that deploys restores no caches; keep `cache: ${{ !startsWith(github.ref, 'refs/tags/') }}` on every cached setup step.
