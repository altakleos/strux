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


def test_codex_runs_as_a_model_alone_from_a_home_of_its_own(monkeypatch, tmp_path):
    monkeypatch.setenv("STRUX_CODEX_HOME", str(tmp_path))
    monkeypatch.setenv("OPENAI_API_KEY", "sk-test")
    monkeypatch.setattr(codex, "Codex", FakeCodex)
    r = codex.judge(request())
    seen = FakeCodex.seen
    assert seen["config"].env["CODEX_HOME"] == str(tmp_path) and "OPENAI_API_KEY" not in seen["config"].env
    start = seen["start"]
    assert start["base_instructions"] == "govern\n\nclose" and start["model"] == "m" and start["ephemeral"]
    assert start["sandbox"] == codex.Sandbox.read_only and start["approval_mode"] == codex.ApprovalMode.deny_all
    assert start["config"]["features"] == {"shell_tool": False, "unified_exec": False}
    assert start["config"]["web_search"] == "disabled" and start["config"]["project_doc_max_bytes"] == 0
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
