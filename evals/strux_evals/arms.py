"""The two arms of a comparison. They differ in one thing: whether strux is installed in the
agent's sandbox. The model, the tasks, the prompt and the permissions are the same."""

import os
from pathlib import Path

ARMS = ("with_strux", "without_strux")

# Where programs installed for the agent live, relative to its working directory.
BIN = "bin"


def strux_binary() -> Path:
    """The strux binary under test, named by STRUX_BIN."""
    path = os.environ.get("STRUX_BIN")
    if not path:
        raise RuntimeError("set STRUX_BIN to the strux binary under test, such as one from go build ./cmd/strux")
    binary = Path(path).resolve()
    if not binary.is_file():
        raise RuntimeError(f"STRUX_BIN {path!r} is not a file")
    return binary


def sandbox_files(arm: str) -> dict[str, str]:
    """The files copied into the sandbox for an arm, as Inspect's Sample.files maps them."""
    if arm not in ARMS:
        raise ValueError(f"unknown arm {arm!r}; arms are {', '.join(ARMS)}")
    if arm == "with_strux":
        return {f"{BIN}/strux": str(strux_binary())}
    return {f"{BIN}/.keep": ""}


def setup_script() -> str:
    """Run in the sandbox before the agent starts; the same for both arms. It does not ignore
    failures, so a harness fault stops the sample instead of looking like strux failing."""
    return f"mkdir -p {BIN} && find {BIN} -type f -exec chmod +x {{}} +"
