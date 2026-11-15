import json
from types import SimpleNamespace

import pytest

from strux_adapters import codex

from .test_protocol import request


class FakeCodex:
    seen: dict = {}

    def __init__(self, config):
        FakeCodex.seen["config"] = config

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def thread_start(self, **kw):
        FakeCodex.seen["start"] = kw
        return self

    def run(self, message, **kw):
        FakeCodex.seen["message"], FakeCodex.seen["run"] = message, kw
        return turn("completed", '{"result":"unknown"}')


def turn(status, final, usage=True):
    total = SimpleNamespace(input_tokens=10, cached_input_tokens=4, output_tokens=3, cache_write_input_tokens=None)
    return SimpleNamespace(
        status=SimpleNamespace(value=status),
        final_response=final,
        usage=SimpleNamespace(total=total) if usage else None,
    )


def ran(args, stdout="", returncode=0, stderr=""):
    return SimpleNamespace(args=args, stdout=stdout, returncode=returncode, stderr=stderr)


CATALOG = {
    "models": [
        {"slug": "m", "tool_mode": "code_mode_only", "multi_agent_version": "v2", "apply_patch_tool_type": "freeform"},
        {"slug": "n", "use_responses_lite": True, "experimental_supported_tools": ["clock"], "context_window": 9},
    ]
}


def test_codex_runs_as_a_model_alone_from_a_home_of_its_own(monkeypatch, tmp_path):
    monkeypatch.setenv("STRUX_CODEX_HOME", str(tmp_path))
    monkeypatch.setenv("OPENAI_API_KEY", "sk-test")
    monkeypatch.setattr(codex, "Codex", FakeCodex)
    monkeypatch.setattr(codex.subprocess, "run", lambda args, **kw: ran(args, json.dumps(CATALOG)))
    r = codex.judge(request())
    seen = FakeCodex.seen
    assert seen["config"].env["CODEX_HOME"] == str(tmp_path) and "OPENAI_API_KEY" not in seen["config"].env
    start = seen["start"]
    assert start["base_instructions"] == "govern\n\nclose" and start["model"] == "m" and start["ephemeral"]
    assert start["sandbox"] == codex.Sandbox.read_only and start["approval_mode"] == codex.ApprovalMode.deny_all
    config = start["config"]
    assert all(on is False for on in config["features"].values()) and {"shell_tool", "apps"} <= set(config["features"])
    assert config["web_search"] == "disabled" and config["project_doc_max_bytes"] == 0
    assert config["model_catalog_json"].endswith("models.json")
    assert start["cwd"] == seen["config"].cwd and "developer_instructions" not in start
    message = seen["message"]
    assert isinstance(message, codex.ExternalMessage) and message.content == "examine"
    assert seen["run"]["output_schema"] == {"type": "object"} and seen["run"]["effort"] == codex.ReasoningEffort.low
    assert (r.stop, r.text, r.input_tokens, r.cache_read_tokens, r.output_tokens) == (
        "done",
        '{"result":"unknown"}',
        6,
        4,
        3,
    )


def test_a_turn_without_an_answer_gives_other():
    assert codex.response(turn("failed", None)).stop == "other"
    assert codex.response(turn("completed", "")).stop == "other"
    assert codex.response(turn("interrupted", "x", usage=False)).stop == "other"


def test_a_missing_home_says_how_to_sign_in(monkeypatch, tmp_path):
    monkeypatch.setenv("STRUX_CODEX_HOME", str(tmp_path / "absent"))
    with pytest.raises(SystemExit, match="mkdir -p -m 700 .* codex login"):
        codex.home()


def test_the_catalog_gives_no_model_tools_of_codex_s_own(monkeypatch):
    calls = []
    monkeypatch.setattr(
        codex.subprocess, "run", lambda args, **kw: calls.append((args, kw)) or ran(args, json.dumps(CATALOG))
    )
    models = codex.catalog({"CODEX_HOME": "h"}, "w", "n")
    assert (
        calls[0][0][1:] == ["debug", "models"]
        and calls[0][1]["env"] == {"CODEX_HOME": "h"}
        and calls[0][1]["cwd"] == "w"
    )
    assert models == [
        {"slug": "m", "experimental_supported_tools": [], "use_responses_lite": False},
        {"slug": "n", "experimental_supported_tools": [], "use_responses_lite": False, "context_window": 9},
    ]


def test_a_model_the_catalog_lacks_is_refused(monkeypatch):
    monkeypatch.setattr(codex.subprocess, "run", lambda args, **kw: ran(args, json.dumps(CATALOG)))
    with pytest.raises(SystemExit, match="no model 'x'"):
        codex.catalog({}, "w", "x")


def test_a_catalog_codex_did_not_give_is_no_response(monkeypatch):
    monkeypatch.setattr(codex.subprocess, "run", lambda args, **kw: ran(args, returncode=1, stderr="offline"))
    with pytest.raises(codex.NoResponse, match="offline"):
        codex.catalog({}, "w", "m")
