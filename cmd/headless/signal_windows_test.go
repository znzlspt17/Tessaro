//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"jevcancode/internal/application"
	"jevcancode/internal/compare"
)

// 2026-11-12: 신호 발신 프로세스만 격리된 자식 콘솔에 붙여 사용자 콘솔에는 영향을 주지 않는다.
func TestOwnedConsoleBreakHelper(t *testing.T) {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "--send-owned-break" {
		t.Skip("helper only")
	}
	pid, err := strconv.ParseUint(args[len(args)-1], 10, 32)
	if err != nil || pid == 0 {
		t.Fatal("invalid child PID")
	}
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	attach := kernel.NewProc("AttachConsole")
	free := kernel.NewProc("FreeConsole")
	handler := kernel.NewProc("SetConsoleCtrlHandler")
	// CREATE_NO_WINDOW의 helper 전용 콘솔에서만 분리한 뒤 대상 콘솔에 붙는다.
	free.Call()
	r, _, e := attach.Call(uintptr(pid))
	if r == 0 {
		t.Fatalf("AttachConsole: %v", e)
	}
	defer free.Call()
	r, _, e = handler.Call(0, 1)
	if r == 0 {
		t.Fatalf("SetConsoleCtrlHandler: %v", e)
	}
	if err := windows.GenerateConsoleCtrlEvent(syscall.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
		t.Fatalf("GenerateConsoleCtrlEvent: %v", err)
	}
}

func TestWindowsConsoleBreakPersistsCancelledRun(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "headless.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	spec, err := filepath.Abs("../../examples/token/specs/loop-factorial.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := compare.Manifest{Engine: "token", Models: []compare.Model{{ID: "fixture", Ranker: "jev", Model: "jev-1.14.0", URL: server.URL}}, Problems: []compare.Problem{{ID: "p", Spec: spec}}, Strategies: []string{"beam"}, Repeats: 1, Seed: 42}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err = os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(dir, "output")
	cmd := exec.Command(exe, "run", "--kind", "compare", "--manifest", manifestPath, "--output", outputDir, "--max-questions", "3", "--progress", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatalf("hidden isolated console start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	select {
	case <-entered:
	case err := <-done:
		finished = true
		t.Fatalf("child exited before request: %v stderr=%s", err, stderr.String())
	case <-time.After(45 * time.Second):
		t.Fatal("child did not reach test server")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	helper := exec.CommandContext(helperCtx, self, "-test.run=^TestOwnedConsoleBreakHelper$", "--", "--send-owned-break", strconv.Itoa(cmd.Process.Pid))
	helper.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("isolated signal helper: %v: %s", err, output)
	}
	select {
	case err := <-done:
		finished = true
		if err == nil {
			t.Fatal("cancelled run exited zero")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("child did not finish cancellation")
	}
	var events []application.Envelope
	var summary struct {
		Kind   string `json:"kind"`
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	for _, line := range bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n")) {
		var marker struct {
			Kind string `json:"kind"`
		}
		if err = json.Unmarshal(line, &marker); err != nil {
			t.Fatalf("invalid JSONL: %v", err)
		}
		if marker.Kind == "summary" {
			if err = json.Unmarshal(line, &summary); err != nil {
				t.Fatal(err)
			}
		} else {
			var event application.Envelope
			if err = json.Unmarshal(line, &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
		}
	}
	if summary.Kind != "summary" || summary.Status != "cancelled" || summary.RunID == "" || len(events) == 0 || events[len(events)-1].Kind != "run_finished" {
		t.Fatalf("summary=%+v events=%d stderr=%s", summary, len(events), stderr.String())
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("missing event at %d: %d", i, ev.Seq)
		}
	}
	mgr := application.New(application.Config{OutputDir: outputDir})
	record, err := mgr.ReadHistory(summary.RunID)
	if err != nil || record.Status != "cancelled" {
		t.Fatalf("persisted status=%s err=%v", record.Status, err)
	}
	trace, err := mgr.ReadTrace(summary.RunID)
	if err != nil || len(trace) != len(events) {
		t.Fatalf("persisted %d vs streamed %d: %v", len(trace), len(events), err)
	}
	t.Logf("owned child %d received CTRL_BREAK_EVENT; cancelled snapshot and %d terminal trace events persisted", cmd.Process.Pid, len(trace))
}
