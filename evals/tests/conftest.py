import os
import subprocess
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]


@pytest.fixture(scope="session", autouse=True)
def strux(tmp_path_factory):
    """The strux binary under test: STRUX_BIN if set, else one built from this checkout."""
    if os.environ.get("STRUX_BIN"):
        return
    binary = tmp_path_factory.mktemp("bin") / "strux"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/strux"], cwd=REPO, check=True)
    os.environ["STRUX_BIN"] = str(binary)
