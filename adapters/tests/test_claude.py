import json
import os

import pytest
from claude_agent_sdk import ClaudeSDKError, ResultMessage

from strux_adapters import claude
from strux_adapters.protocol import NoResponse

from .test_protocol import request


def result(**fields):
    base = {
        "subtype": "success",
        "duration_ms": 1,
        "duration_api_ms": 1,
        "is_error": False,
        "num_turns": 1,
        "session_id": "s",
        "usage": {
            "input_tokens": 5,
            "output_tokens": 2,
            "cache_read_input_tokens": 1,
            "cache_creation_input_tokens": 4,
        },
    }
    return ResultMessage(**{**base, **fields})


def test_claude_code_runs_as_a_model_alone():
    o = claude.options(request(), "/tmp/empty")
    assert o.system_prompt == "govern\n\nclose"
    assert o.tools == [] and o.allowed_tools == [] and o.skills == [] and o.plugins == []
    assert o.setting_sources == [] and o.strict_mcp_config and o.mcp_servers == {}
    assert o.model == "m" and o.effort == "low" and o.cwd == "/tmp/empty"
    assert o.output_format == {"type": "json_schema", "schema": {"type": "object"}}


def test_the_data_is_the_one_user_message(monkeypatch):
    seen = {}

    async def fake_query(*, prompt, options):
        seen["prompt"], seen["options"] = prompt, options
        yield result(structured_output={"result": "unknown"})

    monkeypatch.setattr(claude, "query", fake_query)
    r = claude.judge(request())
    assert seen["prompt"] == "examine" and seen["options"].system_prompt == "govern\n\nclose"
    assert os.path.isabs(seen["options"].cwd) and not os.path.exists(seen["options"].cwd)
    assert (r.stop, json.loads(r.text), r.input_tokens, r.cache_write_tokens) == ("done", {"result": "unknown"}, 5, 4)


@pytest.mark.parametrize(
    "fields,stop",
    [
        ({"stop_reason": "refusal"}, "refused"),
        ({"stop_reason": "max_tokens"}, "length"),
        ({"is_error": True, "api_error_status": 400}, "rejected"),
        ({"subtype": "error_max_turns", "is_error": True}, "other"),
        ({}, "other"),  # an answer outside the schema leaves no structured output
    ],
)
def test_a_result_without_an_answer_gives_its_stop(fields, stop):
    assert claude.response(result(**fields)).stop == stop


@pytest.mark.parametrize(
    "fields",
    [{"is_error": True, "api_error_status": 429}, {"is_error": True, "api_error_status": 529}, {"is_error": True}],
)
def test_a_failure_asking_again_could_cure_is_no_response(fields):
    with pytest.raises(NoResponse):
        claude.response(result(**fields))
    with pytest.raises(NoResponse):
        claude.response(None)


def test_an_sdk_failure_is_no_response(monkeypatch):
    async def failing(*, prompt, options):
        raise ClaudeSDKError("the process ended")
        yield

    monkeypatch.setattr(claude, "query", failing)
    with pytest.raises(NoResponse):
        claude.judge(request())


def test_no_api_key_reaches_claude_code(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-test")
    monkeypatch.setenv("ANTHROPIC_AUTH_TOKEN", "tok")
    seen = {}
    monkeypatch.setattr(claude, "serve", lambda judge: seen.update(env=dict(os.environ)) or 0)
    assert claude.main() == 0
    assert "ANTHROPIC_API_KEY" not in seen["env"] and "ANTHROPIC_AUTH_TOKEN" not in seen["env"]
