"""The comparison reads real Inspect logs, made here by the scripted offline runs."""

import json
import re
from pathlib import Path

import pytest

from strux_evals.compare import Trial, main, read_trials, render, summarize, wilson
from strux_evals.dataset import UNAVAILABLE, expected, load_specs

from .test_wiring import run


def make_logs(root: Path, arm: str, answers: list[str]) -> Path:
    directory = root / arm
    for i, answer in enumerate(answers):
        run(arm, answer, directory / str(i))
    return directory


def test_wilson_matches_known_values():
    low, high = wilson(8, 10)
    assert (round(low, 3), round(high, 3)) == (0.490, 0.943)
    assert wilson(0, 5)[0] == 0.0 and wilson(5, 5)[1] == 1.0
    with pytest.raises(ValueError):
        wilson(0, 0)


def test_two_arms_side_by_side(tmp_path):
    version = expected(load_specs()[0], "with_strux")
    with_ = make_logs(tmp_path, "with_strux", [version, version, "wrong"])
    without = make_logs(tmp_path, "without_strux", [UNAVAILABLE, "v9.9.9"])
    (group,) = summarize(read_trials([with_, without]))
    assert group.task == "strux_stub" and group.agent == "offline"
    assert (group.arms["with_strux"].passes, group.arms["with_strux"].trials) == (2, 3)
    assert (group.arms["without_strux"].passes, group.arms["without_strux"].trials) == (1, 2)
    assert group.difference == pytest.approx(2 / 3 - 1 / 2)
    text = render([group])
    assert "2/3 pass = 67%" in text and "1/2 pass = 50%" in text
    assert "intervals overlap" in text


def test_a_missing_arm_is_reported_not_compared(tmp_path):
    version = expected(load_specs()[0], "with_strux")
    (group,) = summarize(read_trials([make_logs(tmp_path, "with_strux", [version])]))
    assert group.difference is None
    assert re.search(r"without_strux\s+\(no runs\)", render([group]))


def test_errored_trials_are_not_failures():
    group = ("t", "a", "1.2.3", "m")
    trials = [
        Trial(group, "with_strux", True, False, 10, 1.0),
        Trial(group, "with_strux", False, True, 0, 0.0),  # a sandbox fault
    ]
    stats = summarize(trials)[0].arms["with_strux"]
    assert (stats.trials, stats.errors, stats.passes, stats.rate) == (1, 1, 1, 1.0)


def test_different_agents_are_different_groups():
    a, b = ("t", "claude_code", "1.2.3", "m"), ("t", "codex_cli", "1.2.3", "m")
    trials = [Trial(a, "with_strux", True, False, 1, 1.0), Trial(b, "without_strux", True, False, 1, 1.0)]
    groups = summarize(trials)
    assert len(groups) == 2 and all(g.difference is None for g in groups)


def test_command_line(tmp_path, capsys):
    version = expected(load_specs()[0], "with_strux")
    with_ = make_logs(tmp_path, "with_strux", [version])
    without = make_logs(tmp_path, "without_strux", [UNAVAILABLE])
    assert main([str(with_), str(without), "--json"]) == 0
    (group,) = json.loads(capsys.readouterr().out)
    assert group["arms"]["with_strux"]["rate"] == 1.0 and group["difference"] == 0.0
    assert main([str(tmp_path / "nothing")]) == 1
    assert "compare:" in capsys.readouterr().err
