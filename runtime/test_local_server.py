import unittest
import json
import threading
import urllib.error
import urllib.request
from http.server import HTTPServer

from local_server import handler_for, validate_request


def request():
    return {"state": {"intent": "add"}, "questions": {"rank": {
        "type": "choice", "instructions": "Pick one", "criteria": {"a": "a+b", "b": "a-b"}}}}


class RequestTests(unittest.TestCase):
    def test_accepts_choice_state_without_mutation(self):
        value = request()
        self.assertIs(validate_request(value), value)

    def test_rejects_invalid_or_excessive_candidates(self):
        for criteria in ({}, {"a": "one"}, {str(i): "x" for i in range(256)}, {"a": None, "b": "ok"}):
            value = request()
            value["questions"]["rank"]["criteria"] = criteria
            with self.assertRaises(ValueError):
                validate_request(value)


class HTTPTests(unittest.TestCase):
    def setUp(self):
        class Decider:
            name = "test-model"

            def system_one(self, state, questions, **kwargs):
                if state == "fail":
                    raise RuntimeError("untrusted model diagnostic")
                return {"model": self.name, "answers": {next(iter(questions)): {
                    "type": "choice", "choice": "a", "probabilities": {"a": 0.8, "b": 0.2}}}}

        self.server = HTTPServer(("127.0.0.1", 0), handler_for(Decider()))
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def post(self, body):
        value = urllib.request.Request(self.base + "/v1/systemone", data=json.dumps(body).encode(),
                                       headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(value, timeout=3) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as response:
            return response.code, json.load(response)

    def test_choice_wire_roundtrip_and_validation(self):
        status, result = self.post(request())
        self.assertEqual(status, 200)
        self.assertEqual(result["answers"]["rank"]["choice"], "a")
        self.assertEqual(self.post({})[0], 422)

    def test_inference_error_does_not_echo_diagnostic(self):
        value = request()
        value["state"] = "fail"
        self.assertEqual(self.post(value), (500, {"error": "model_inference_failed"}))

    def test_rejects_unsupported_questions_or_missing_state(self):
        for value in ({}, {"state": None, "questions": {}}, {"state": {}, "questions": {"x": {"type": "score"}}}):
            with self.assertRaises(ValueError):
                validate_request(value)


if __name__ == "__main__":
    unittest.main()
