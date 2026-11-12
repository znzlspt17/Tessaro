package rank

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestWorkerRunsAndReusesProcess(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	python := filepath.Join(filepath.Dir(current), "..", "..", ".venv-rocm72", "Scripts", "python.exe")
	python, e := exec.LookPath(python)
	if e != nil {
		t.Skip("python not available")
	}
	script := filepath.Join(t.TempDir(), "worker.py")
	source := `import sys,json
print(json.dumps({'version':1,'type':'ready'}),flush=True)
count=0
for line in sys.stdin:
 count += 1
 req=json.loads(line)
 print(json.dumps({'version':1,'type':'result','id':req['id'],'result':{'model':'fixture','usage':{'input_tokens':count,'output_tokens':1},'answers':{'pick':{'type':'choice','choice':'a','probabilities':{'a':0.75,'b':0.25}}}}}),flush=True)
`
	if e = os.WriteFile(script, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	w := NewWorker(WorkerConfig{Python: python, Script: script, ModelDir: t.TempDir(), Model: "fixture", StartupTimeout: time.Second * 5})
	defer w.Close()
	if e = w.Prepare(context.Background()); e != nil {
		t.Fatalf("prepare: %v", e)
	}
	cs := []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}}
	for i := 0; i < 2; i++ {
		scores, u, e := w.RankWithUsage(context.Background(), "intent", cs)
		if e != nil || len(scores) != 2 || u == nil || u.InputTokens == nil || *u.InputTokens != i+1 {
			t.Fatalf("rank %d: %v %+v %+v", i, e, scores, u)
		}
	}
	if w.Metadata().ReturnedModel != "fixture" {
		t.Fatal("missing returned model")
	}
}

func TestWorkerDecisionPreservesExplicitChoice(t *testing.T) {
	worker := fakePython(t, `import sys,json
print(json.dumps({'version':1,'type':'ready'}),flush=True)
for line in sys.stdin:
 req=json.loads(line)
 print(json.dumps({'version':1,'type':'result','id':req['id'],'result':{'model':'fixture','answers':{'pick':{'type':'choice','choice':'b','probabilities':{'a':0.5,'b':0.5}}}}}),flush=True)
`, 5*time.Second, 5*time.Second)
	d, err := worker.RankDecision(context.Background(), "intent", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}})
	if err != nil || d.ChosenID != "b" || len(d.Scores) != 2 {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}

func fakePython(t *testing.T, source string, startup, request time.Duration) *Worker {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	python := filepath.Join(filepath.Dir(current), "..", "..", ".venv-rocm72", "Scripts", "python.exe")
	if _, err := os.Stat(python); err != nil {
		t.Skip("project Python unavailable")
	}
	script := filepath.Join(t.TempDir(), "worker.py")
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(WorkerConfig{Python: python, Script: script, ModelDir: t.TempDir(), StartupTimeout: startup, RequestTimeout: request})
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestWorkerStartupTimeoutAndInvalidFrames(t *testing.T) {
	cases := []struct {
		name, source string
		timeout      time.Duration
	}{
		{"timeout", "import time\ntime.sleep(5)\n", 100 * time.Millisecond},
		{"unterminated", "import sys\nsys.stdout.write('{\"version\":1,\"type\":\"ready\"}')\nsys.stdout.flush()\n", 5 * time.Second},
		{"bad ready", "import sys\nprint('not-json',flush=True)\n", 5 * time.Second},
		{"oversized", "import sys\nprint('x'*(1024*1024+1),flush=True)\n", 5 * time.Second},
		{"duplicate key", "print('{\"version\":1,\"version\":1,\"type\":\"ready\"}',flush=True)\n", 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := fakePython(t, tc.source, tc.timeout, time.Second)
			if err := w.Prepare(context.Background()); err == nil {
				t.Fatal("invalid ready accepted")
			}
		})
	}
}

func TestWorkerRejectsWrongIDAndInvalidDistribution(t *testing.T) {
	cases := []struct{ name, response string }{
		{"id", `{"version":1,"type":"result","id":999,"result":{"model":"x","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.5,"b":0.5}}}}}`},
		{"missing probability", `{"version":1,"type":"result","id":1,"result":{"model":"x","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":1}}}}}`},
		{"duplicate probability", `{"version":1,"type":"result","id":1,"result":{"model":"x","answers":{"pick":{"type":"choice","choice":"a","probabilities":{"a":0.5,"a":0.5,"b":0.5}}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Explicit JSON encoding prevents fixture text from being interpreted by Python.
			source := "import sys\nprint(\"{\\\"version\\\":1,\\\"type\\\":\\\"ready\\\"}\",flush=True)\nsys.stdin.readline()\nprint(" + strconv.Quote(tc.response) + ",flush=True)\n"
			w := fakePython(t, source, 5*time.Second, 5*time.Second)
			_, err := w.Rank(context.Background(), "x", []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestWorkerCancelsAndRestarts(t *testing.T) {
	source := `import json,sys,time
print(json.dumps({'version':1,'type':'ready'}),flush=True)
for line in sys.stdin:
 req=json.loads(line)
 if req['payload']['state']=='slow': time.sleep(5)
 print(json.dumps({'version':1,'type':'result','id':req['id'],'result':{'model':'fixture','answers':{'pick':{'type':'choice','choice':'a','probabilities':{'a':.5,'b':.5}}}}}),flush=True)
`
	w := fakePython(t, source, 5*time.Second, 5*time.Second)
	cs := []Candidate{{ID: "a", Code: "a"}, {ID: "b", Code: "b"}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := w.Rank(ctx, "slow", cs); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := w.Rank(context.Background(), "fast", cs); err != nil {
		t.Fatalf("restart: %v", err)
	}
}
