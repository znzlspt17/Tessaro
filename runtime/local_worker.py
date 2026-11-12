"""2026-11-07: Decider를 로컬 포트 없이 앱 소유 표준 입출력 워커로 실행한다."""
import argparse
import json
import os
import sys

MAX_FRAME = 1 << 20


def send(out, value):
    data = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode("utf-8")
    if len(data) > MAX_FRAME:
        raise ValueError("frame too large")
    out.write(data + b"\n")
    out.flush()


def validate_request(body):
    if not isinstance(body, dict) or not isinstance(body.get("state"), (str, dict, list)):
        raise ValueError("invalid state")
    questions = body.get("questions")
    if not isinstance(questions, dict) or set(questions) != {"pick"}:
        raise ValueError("invalid question")
    question = questions["pick"]
    if not isinstance(question, dict) or question.get("type") != "choice":
        raise ValueError("invalid choice")
    if not isinstance(question.get("instructions"), (str, dict, list)):
        raise ValueError("invalid instructions")
    criteria = question.get("criteria")
    if not isinstance(criteria, dict) or not 2 <= len(criteria) <= 255:
        raise ValueError("invalid criteria")
    if any(not isinstance(k, str) or not k or not isinstance(v, (str, dict, list)) for k, v in criteria.items()):
        raise ValueError("invalid criteria")
    return body


def serve(decider, inp, out):
    send(out, {"version": 1, "type": "ready"})
    while True:
        line = inp.readline(MAX_FRAME + 2)
        if not line:
            return
        request_id = 0
        try:
            if len(line) > MAX_FRAME + 1 or not line.endswith(b"\n"):
                return
            frame = json.loads(line.decode("utf-8"))
            if not isinstance(frame, dict) or frame.get("version") != 1 or frame.get("type") != "rank":
                return
            request_id = frame.get("id")
            if type(request_id) is not int or request_id <= 0:
                return
            payload = validate_request(frame.get("payload"))
            # batch=1 제약과 기존 offline 추론 제한을 그대로 유지한다.
            result = decider.system_one(payload["state"], payload["questions"], max_state_tokens=8192)
            send(out, {"version": 1, "type": "result", "id": request_id, "result": result})
        except Exception:
            # 모델의 예외나 입력 문맥은 IPC로 되돌리지 않는다.
            try:
                send(out, {"version": 1, "type": "error", "id": request_id, "code": "model_inference_failed"})
            except Exception:
                pass
            return


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-dir", required=True)
    args = parser.parse_args()
    # 라이브러리의 Python/C stdout도 프로토콜 스트림과 분리한다.
    protocol = os.fdopen(os.dup(sys.stdout.fileno()), "wb", buffering=0)
    os.dup2(sys.stderr.fileno(), sys.stdout.fileno())
    try:
        from gpu_probe import load_decider
        model = load_decider(args.model_dir)
        serve(model, sys.stdin.buffer, protocol)
    except Exception:
        try:
            send(protocol, {"version": 1, "type": "error", "code": "model_initialization_failed"})
        except Exception:
            pass
    finally:
        protocol.close()


if __name__ == "__main__":
    main()
