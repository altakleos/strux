# Strux

Strux keeps agent work accountable. Applications describe their concepts as versioned definitions; Strux checks content against them, keeps every result bound to exactly what was examined, and lets only authorized, signed changes count, so anyone relying on a result can verify its basis later.

**Status:** a skeleton. `strux` describes itself and reports its version, and every change goes through the full release pipeline.

## Install

On macOS and Linux:

```sh
curl -fsSL https://github.com/altakleos/strux/releases/latest/download/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://github.com/altakleos/strux/releases/latest/download/install.ps1 | iex
```

Each release from v0.0.2 carries these scripts, and each script installs its own release: to install another, replace `latest/download` with `download/vX.Y.Z`. The script checks the archive's SHA-256 against the release's `checksums.txt` and, when [gh](https://cli.github.com) is installed and logged in, the archive's build provenance. It installs strux into `~/.local/bin`, or the directory in `STRUX_INSTALL_DIR`, and says if that directory is not on your `PATH`. `STRUX_RELEASE_URL` downloads the release's assets from a mirror instead.

To pin strux in a project, use [mise](https://mise.jdx.dev): `mise use github:altakleos/strux@X.Y.Z` records the version in `mise.toml`, and mise checks each download's digest and build provenance.

Or download the archive for your platform from the [latest release](https://github.com/altakleos/strux/releases/latest): `strux_<version>_<os>_<arch>.tar.gz`, or `.zip` on Windows, where `<version>` is the release without its `v`, `<os>` is `linux`, `darwin` or `windows`, and `<arch>` is `amd64` or `arm64`. Then check that the release workflow built it:

```sh
gh attestation verify strux_<version>_<os>_<arch>.tar.gz --repo altakleos/strux \
  --signer-workflow altakleos/strux/.github/workflows/release.yml
```

Or build it with Go 1.27 or later, which gives a binary of your own rather than the release's:

```sh
go install github.com/altakleos/strux/cmd/strux@latest
```

Release archives are reproducible: building a release's tag gives the same archives, byte for byte, on any machine.

## Uninstall

The install scripts write one file and change nothing else: no shell profile, no registry. Delete it:

```sh
rm ~/.local/bin/strux        # or "$STRUX_INSTALL_DIR/strux" if you set that
```

```powershell
Remove-Item "$HOME\.local\bin\strux.exe"   # or "$env:STRUX_INSTALL_DIR\strux.exe"
```

If you added the install directory to your `PATH` yourself, you can leave it; an empty directory on `PATH` does no harm. A strux installed another way is removed the way it was installed: with mise, remove it from `mise.toml` and run `mise uninstall github:altakleos/strux`; with `go install`, delete `strux` from `$(go env GOBIN)`, or from `$(go env GOPATH)/bin` when that is empty; from a downloaded archive, delete the files you extracted.

## Use

```sh
strux                   # what strux is, and its commands
strux help <command>    # what a command does, and its flags
strux version           # this build's version, commit and platform
```

Every result is YAML, or JSON with `--json`; both carry the same fields. The exit status is 0 on success, 1 on failure and 2 for an invalid command line.

## Develop

`make check` runs the checks CI runs that one machine can; see [AGENTS.md](https://github.com/altakleos/strux/blob/main/AGENTS.md).

## License

Copyright 2026 AltaKleos, LLC. Licensed under the Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). Each release archive also carries THIRD_PARTY_NOTICES, the licenses and notices of the code strux includes from other projects.
