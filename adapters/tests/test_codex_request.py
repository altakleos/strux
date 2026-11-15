"""The request the bundled Codex sends for a judgment, captured by a local server in place of the
model's provider: the governing text and the data, and no tool. It signs in nowhere."""

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from strux_adapters import codex
from strux_adapters.protocol import Request

ANSWER = '{"lines":3}'


def events() -> bytes:
    def event(kind, **fields):
        return f"event: {kind}\ndata: {json.dumps({'type': kind, **fields})}\n\n"

    item = {
        "type": "message",
        "role": "assistant",
        "id": "msg_1",
        "content": [{"type": "output_text", "text": ANSWER, "annotations": []}],
    }
    usage = {
        "input_tokens": 40,
        "input_tokens_details": {"cached_tokens": 0},
        "output_tokens": 5,
        "output_tokens_details": {"reasoning_tokens": 0},
        "total_tokens": 45,
    }
    return (
        event("response.created", response={"id": "resp_1"})
        + event("response.output_item.done", output_index=0, item=item)
        + event("response.completed", response={"id": "resp_1", "usage": usage})
    ).encode()


@pytest.fixture
def provider(monkeypatch, tmp_path):
    """A local server standing in for the model's provider, which records each request."""
    seen: list[tuple[str, str, bytes]] = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            seen.append(("GET", self.path, b""))
            self.send_response(404)
            self.end_headers()

        def do_POST(self):
            seen.append(("POST", self.path, self.rfile.read(int(self.headers.get("content-length") or 0))))
            body = events()
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    local = {
        "name": "local",
        "base_url": f"http://127.0.0.1:{server.server_address[1]}/v1",
        "wire_api": "responses",
        "requires_openai_auth": False,
        "request_max_retries": 0,
        "stream_max_retries": 0,
    }
    config = {**codex.CONFIG, "model_provider": "local", "model_providers": {"local": local}}
    config["features"] = {**config["features"], "enable_request_compression": False}
    monkeypatch.setattr(codex, "CONFIG", config)
    monkeypatch.setenv("STRUX_CODEX_HOME", str(tmp_path))
    yield seen
    server.shutdown()


def test_codex_sends_the_governing_text_and_the_data_and_no_tool(provider, tmp_path):
    schema = {
        "type": "object",
        "properties": {"lines": {"type": "integer"}},
        "required": ["lines"],
        "additionalProperties": False,
    }
    model = codex.read_catalog(codex.environment(codex.home()), str(tmp_path))[0]["slug"]
    r = codex.judge(
        Request(
            model=model, effort="low", max_tokens=100, system="govern", data="a\nb\nc", reminder="close", schema=schema
        )
    )
    posts = [json.loads(body) for method, path, body in provider if method == "POST"]
    assert len(posts) == 1, provider
    sent = posts[0]
    assert sent["model"] == model
    assert not sent.get("tools") and not sent.get("instructions")
    assert [(i["type"], i.get("role")) for i in sent["input"]] == [
        ("message", "developer"),
        ("function_call_output", None),
    ]
    assert [c["text"] for c in sent["input"][0]["content"]] == ["govern\n\nclose"]
    assert sent["input"][1]["output"] == "a\nb\nc"
    assert sent["text"]["format"]["schema"] == schema
    assert (r.stop, r.text) == ("done", ANSWER)
