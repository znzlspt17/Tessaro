import io
import json
import unittest
from local_worker import serve

class Fake:
    name = "fake"
    def system_one(self, state, questions, **kwargs):
        return {"model": self.name, "answers": {"pick": {"type": "choice", "choice": "a", "probabilities": {"a": .8, "b": .2}}}}

class Failing:
    def system_one(self, state, questions, **kwargs):
        raise RuntimeError("private diagnostic must stay hidden")


class WorkerTests(unittest.TestCase):
    def test_ready_and_single_rank(self):
        request = {"version": 1, "type": "rank", "id": 1, "payload": {"state":"x", "questions":{"pick":{"type":"choice", "instructions":"choose", "criteria":{"a":"x", "b":"y"}}}}}
        inp = io.BytesIO((json.dumps(request) + "\n").encode())
        out = io.BytesIO()
        serve(Fake(), inp, out)
        frames = [json.loads(line) for line in out.getvalue().splitlines()]
        self.assertEqual(frames[0], {"version":1,"type":"ready"})
        self.assertEqual(frames[1]["id"], 1)
        self.assertEqual(frames[1]["result"]["answers"]["pick"]["choice"], "a")

    def test_inference_failure_returns_only_safe_code(self):
        request = {"version": 1, "type": "rank", "id": 7, "payload": {"state":"x", "questions":{"pick":{"type":"choice", "instructions":"choose", "criteria":{"a":"x", "b":"y"}}}}}
        out = io.BytesIO()
        serve(Failing(), io.BytesIO((json.dumps(request) + "\n").encode()), out)
        frames = [json.loads(line) for line in out.getvalue().splitlines()]
        self.assertEqual(frames[1], {"version":1, "type":"error", "id":7, "code":"model_inference_failed"})
        self.assertNotIn(b"private diagnostic", out.getvalue())

    def test_bad_framing_is_not_processed(self):
        out = io.BytesIO()
        serve(Fake(), io.BytesIO(b'{"version":1'), out)
        frames = [json.loads(line) for line in out.getvalue().splitlines()]
        self.assertEqual(frames, [{"version":1,"type":"ready"}])

if __name__ == "__main__":
    unittest.main()
