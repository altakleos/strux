"""The two arms of a comparison. They differ in one thing: whether strux is installed in the
agent's sandbox. The model, the tasks, the prompt and the permissions are the same."""

import os
from pathlib import Path

ARMS = ("with_strux", "without_strux")

# Where programs installed for the agent live. The local sandbox is a temporary directory on the host
# and writes an absolute path to the host itself, so there it is relative to that directory. A Docker
# sandbox has no say in its working directory, which may be /, so there it is a directory of our own.
LOCAL_BIN = "bin"
DOCKER_WORKDIR = "/work"
DOCKER_BIN = f"{DOCKER_WORKDIR}/bin"


def bin_dir(sandbox: str) -> str:
    return DOCKER_BIN if sandbox == "docker" else LOCAL_BIN


def strux_binary() -> Path:
    """The strux binary under test, named by STRUX_BIN."""
    path = os.environ.get("STRUX_BIN")
    if not path:
        raise RuntimeError("set STRUX_BIN to the strux binary under test, such as one from go build ./cmd/strux")
    binary = Path(path).resolve()
    if not binary.is_file():
        raise RuntimeError(f"STRUX_BIN {path!r} is not a file")
    return binary


def is_elf(path: Path) -> bool:
    """Whether path is a Linux executable, which is what a Docker sandbox can run."""
    with path.open("rb") as f:
        return f.read(4) == b"\x7fELF"


def sandbox_files(arm: str, bin: str = LOCAL_BIN) -> dict[str, str]:
    """The files copied into the sandbox for an arm, as Inspect's Sample.files maps them."""
    if arm not in ARMS:
        raise ValueError(f"unknown arm {arm!r}; arms are {', '.join(ARMS)}")
    if arm == "with_strux":
        return {f"{bin}/strux": str(strux_binary())}
    return {f"{bin}/.keep": ""}


def setup_script(bin: str = LOCAL_BIN) -> str:
    """Run in the sandbox before the agent starts; the same for both arms. It does not ignore
    failures, so a harness fault stops the sample instead of looking like strux failing."""
    return f"mkdir -p {bin} && find {bin} -type f -exec chmod +x {{}} +"
