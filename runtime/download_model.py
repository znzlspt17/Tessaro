"""2026-11-05: 인증정보를 사용하지 않고 공개된 고정 revision만 다운로드한다."""
from pathlib import Path

from huggingface_hub import snapshot_download


REVISION = "585473dec4fa93f0325c2bf02dda7273b61f44c1"


if __name__ == "__main__":
    destination = Path(__file__).resolve().parent / "models" / "decider-4b"
    snapshot_download("Mapika/decider-4b", revision=REVISION, token=False,
                      local_dir=str(destination),
                      allow_patterns=["*.json", "*.safetensors", "*.jinja", "*.txt", "*.model"],
                      ignore_patterns=["vision/*"])
    print(f"Mapika/decider-4b revision={REVISION}")
