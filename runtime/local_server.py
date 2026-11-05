"""2026-11-05: Windows ROCm eager 경로를 loopback HTTP로 제공한다."""
import argparse
import json
from http.server import BaseHTTPRequestHandler, HTTPServer


def validate_request(body):
    if not isinstance(body, dict) or not isinstance(body.get("state"), (str, dict, list)):
        raise ValueError("state must be a string, object or array")
    questions = body.get("questions")
    if not isinstance(questions, dict) or len(questions) != 1:
        raise ValueError("one Choice question is required")
    question = next(iter(questions.values()))
    if not isinstance(question, dict) or question.get("type") != "choice":
        raise ValueError("only Choice questions are supported")
    if not isinstance(question.get("instructions"), (str, dict, list)):
        raise ValueError("instructions are required")
    criteria = question.get("criteria")
    if not isinstance(criteria, dict) or not 2 <= len(criteria) <= 255:
        raise ValueError("Choice requires 2..255 candidates")
    if any(not isinstance(k, str) or not k or not isinstance(v, (str, dict, list))
           for k, v in criteria.items()):
        raise ValueError("invalid criteria")
    return body


def handler_for(decider):
    class Handler(BaseHTTPRequestHandler):
        def setup(self):
            super().setup()
            self.connection.settimeout(60)

        def log_message(self, format, *args):
            pass  # 코드 문맥이나 HTTP 요청을 로그에 복제하지 않는다.

        def respond(self, status, value):
            data = json.dumps(value, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            if self.path == "/health":
                self.respond(200, {"ready": True, "device": "cuda:0", "engine": "eager", "model": decider.name})
            else:
                self.respond(404, {"error": "not_found"})

        def do_POST(self):
            if self.path != "/v1/systemone":
                self.respond(404, {"error": "not_found"})
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= 1_048_576:
                    self.respond(413, {"error": "request_size"})
                    return
                body = validate_request(json.loads(self.rfile.read(length)))
            except (ValueError, UnicodeError):
                self.respond(422, {"error": "invalid_choice_request"})
                return
            try:
                # 요청 순차 처리로 Windows ROCm의 batch=1 조건을 유지한다.
                result = decider.system_one(body["state"], body["questions"], max_state_tokens=8192)
                self.respond(200, result)
            except Exception:
                self.respond(500, {"error": "model_inference_failed"})
    return Handler


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-dir", required=True)
    parser.add_argument("--port", type=int, default=8000)
    args = parser.parse_args()
    from gpu_probe import load_decider

    model = load_decider(args.model_dir)
    server = HTTPServer(("127.0.0.1", args.port), handler_for(model))
    print(json.dumps({"ready": True, "port": args.port, "device": "cuda:0"}), flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
