# Strux

Strux keeps agent work accountable. Applications describe their concepts as versioned definitions; Strux checks content against them, keeps every result bound to exactly what was examined, and lets only authorized, signed changes count, so anyone relying on a result can verify its basis later.

**Status:** a skeleton. `strux` describes itself and reports its version, and every change goes through the full release pipeline.

## Install

Download the archive for your platform from the [latest release](https://github.com/altakleos/strux/releases/latest): `strux_<version>_<os>_<arch>.tar.gz`, or `.zip` on Windows, where `<version>` is the release without its `v`, `<os>` is `linux`, `darwin` or `windows`, and `<arch>` is `amd64` or `arm64`. Then check that the release workflow built it:

```sh
gh attestation verify strux_<version>_<os>_<arch>.tar.gz --repo altakleos/strux \
  --signer-workflow altakleos/strux/.github/workflows/release.yml
```

Or build it with Go 1.27 or later:

```sh
go install github.com/altakleos/strux/cmd/strux@latest
```

Release archives are reproducible: building a release's tag gives the same archives, byte for byte, on any machine.

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
