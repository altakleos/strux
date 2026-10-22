import json
from pathlib import Path

import pytest

from strux_evals.baseline import (
    MIN_TRIALS,
    Entry,
    check,
    entries_of,
    fisher_lower,
    main,
    read,
    write,
)
from strux_evals.compare import Trial
from strux_evals.dataset import expected, load_specs

from .test_compare import make_logs

GROUP = ("t", "claude_code", "1.2.3", "m")


def entry(passes: int, trials: int, group=GROUP) -> Entry:
    return Entry(*group, passes=passes, trials=trials)


def test_fisher_matches_hand_computed_values():
    assert fisher_lower(1, 10, 9, 10) == pytest.approx(101 / 184756)  # 9/10 fell to 1/10: (1 + 10 * 10) / C(20, 10)
    assert fisher_lower(8, 10, 9, 10) == pytest.approx(0.5)
    assert fisher_lower(0, 5, 5, 5) == pytest.approx(1 / 252)
    assert fisher_lower(5, 5, 5, 5) == pytest.approx(1.0)
    assert fisher_lower(10, 10, 5, 10) > 0.99  # better than the baseline is never a regression
    with pytest.raises(ValueError):
        fisher_lower(6, 5, 5, 5)


def test_an_unchanged_run_passes():
    (v,) = check([entry(9, 10)], [entry(9, 10)])
    assert v.ok and "not lower" in v.message


def test_a_large_drop_is_a_regression():
    (v,) = check([entry(9, 10)], [entry(3, 10)])
    assert not v.ok and v.message.startswith("regression: 3/10 = 30% against a baseline of 9/10 = 90%")


def test_a_small_drop_is_within_chance():
    assert check([entry(9, 10)], [entry(7, 10)])[0].ok


def test_too_few_trials_cannot_be_checked():
    (v,) = check([entry(9, 10)], [entry(MIN_TRIALS - 1, MIN_TRIALS - 1)])
    assert not v.ok and "at least" in v.message


def test_a_missing_group_fails_and_an_extra_one_is_ignored():
    other = ("t", "codex_cli", "1.2.3", "m")
    verdicts = check([entry(9, 10)], [entry(9, 10, other)])
    assert [v.ok for v in verdicts] == [False] and "no trials" in verdicts[0].message


def test_only_scored_with_strux_trials_count():
    trials = [
        Trial(GROUP, "with_strux", True, False, 1, 1.0),
        Trial(GROUP, "with_strux", False, False, 1, 1.0),
        Trial(GROUP, "with_strux", False, True, 0, 0.0),  # errored: not a failure
        Trial(GROUP, "without_strux", True, False, 1, 1.0),  # the other arm
    ]
    assert entries_of(trials) == [entry(1, 2)]


def test_baseline_file_round_trips(tmp_path):
    path = tmp_path / "baselines" / "main.json"
    write(path, [entry(9, 10)], "strux v0.1.0")
    assert read(path) == [entry(9, 10)]
    assert json.loads(path.read_text())["note"] == "strux v0.1.0"
    with pytest.raises(ValueError, match="no with_strux trials"):
        write(path, [], "")


@pytest.mark.parametrize("text", ["not json", "{}", '{"version": 2, "arm": "with_strux", "groups": []}'])
def test_unreadable_baselines_are_errors(tmp_path, text):
    path = tmp_path / "b.json"
    path.write_text(text)
    with pytest.raises(ValueError):
        read(path)


def test_command_line_end_to_end(tmp_path: Path, capsys):
    version = expected(load_specs()[0], "with_strux")
    good = make_logs(tmp_path / "good", "with_strux", [version] * 10)
    bad = make_logs(tmp_path / "bad", "with_strux", ["wrong"] * 10)
    baseline = tmp_path / "baselines" / "main.json"

    assert main(["record", str(baseline), str(good), "--note", "known good"]) == 0
    assert main(["check", str(baseline), str(good)]) == 0
    assert "ok" in capsys.readouterr().out.splitlines()[-1]
    assert main(["check", str(baseline), str(bad)]) == 1
    assert "FAIL" in capsys.readouterr().out
    assert main(["check", str(tmp_path / "missing.json"), str(good)]) == 2
    assert main([]) == 2
