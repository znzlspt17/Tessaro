"""2026-11-05: CPU 대체 없이 ROCm GPU 연산과 선택형 모델 반복 추론을 검증한다."""
import argparse
import importlib.metadata
import json
import math
import os
import platform
import time

import torch


def load_decider(model_dir):
    # 다운로드와 추론을 분리해 추론 과정에서 인증정보나 네트워크를 사용하지 않는다.
    os.environ["HF_HUB_OFFLINE"] = "1"
    os.environ["HF_HUB_DISABLE_IMPLICIT_TOKEN"] = "1"
    from decider.infer import Decider

    if not torch.cuda.is_available() or not torch.version.hip:
        raise RuntimeError("ROCm GPU is required; CPU fallback is disabled")
    # Windows ROCm에서는 Triton/그래프 대신 Transformers의 PyTorch 참조 경로를 사용한다.
    return Decider(model_dir, device="cuda:0", dtype=torch.float16, use_graphs=False)


def probe(model_dir=None, repeats=3):
    if not torch.cuda.is_available() or not torch.version.hip:
        raise RuntimeError("ROCm GPU is required; CPU fallback is disabled")
    x = torch.arange(16, device="cuda:0", dtype=torch.float32).reshape(4, 4)
    y = x @ x
    torch.cuda.synchronize()
    expected = torch.tensor([[56, 62, 68, 74], [152, 174, 196, 218],
                             [248, 286, 324, 362], [344, 398, 452, 506]], dtype=torch.float32)
    if not torch.equal(y.cpu(), expected):
        raise RuntimeError("GPU matrix multiplication returned an unexpected result")
    report = {"python": platform.python_version(), "platform": platform.platform(),
              "torch": torch.__version__, "hip": torch.version.hip,
              "gpu": torch.cuda.get_device_name(0), "matrix_passed": True,
              "model_verified": False}
    if model_dir:
        d = load_decider(model_dir)
        devices = {str(p.device) for p in d.m.parameters()}
        if not devices or any(not v.startswith("cuda") for v in devices):
            raise RuntimeError("Model parameters are not entirely on the GPU")
        questions = {"rank": {"type": "choice", "instructions": "Select the Go expression that adds a and b.",
                               "criteria": {"add": "a + b", "mul": "a * b", "sub": "a - b", "div": "a / b"}}}
        results = []
        for _ in range(repeats):
            start = time.perf_counter()
            with torch.inference_mode():
                answer = d.system_one({"intent": "Return the sum of the two integer parameters a and b."}, questions)
            torch.cuda.synchronize()
            probabilities = answer["answers"]["rank"]["probabilities"]
            if (set(probabilities) != {"add", "mul", "sub", "div"}
                    or any(not math.isfinite(p) or not 0 <= p <= 1 for p in probabilities.values())
                    or abs(sum(probabilities.values()) - 1) > 1e-4):
                raise RuntimeError("Invalid decision distribution")
            results.append({"seconds": time.perf_counter() - start, "answer": answer})
        report.update(model_verified=True, model="Mapika/decider-4b", devices=sorted(devices),
                      dtype="float16", engine="eager PyTorch reference (no FLA/Triton)",
                      decider=importlib.metadata.version("decider-ai"),
                      transformers=importlib.metadata.version("transformers"),
                      peak_gpu_bytes=torch.cuda.max_memory_allocated(), results=results)
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-dir")
    parser.add_argument("--repeats", type=int, default=3)
    args = parser.parse_args()
    if args.repeats < 1:
        parser.error("--repeats must be positive")
    print(json.dumps(probe(args.model_dir, args.repeats), ensure_ascii=False, indent=2))
