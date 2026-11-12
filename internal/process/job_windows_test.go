//go:build windows

package process

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 2026-11-07: 실제 Windows에서 부모 강제 종료 시 상속된 자식도 종료되는지 확인한다.
func TestAppJobKillsInheritedChildOnParentTermination(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, exe, "-test.run=^TestJobHelperProcess$")
	parent.Env = []string{"JEVCANCODE_JOB_HELPER=parent"}
	pipe, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	line, err := bufio.NewReader(pipe).ReadString('\n')
	if err != nil {
		t.Fatalf("helper readiness: %v", err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
	if err != nil {
		t.Fatalf("helper did not report child pid")
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if err = parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if state, err := windows.WaitForSingleObject(child, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned child survived parent termination: state=%d error=%v", state, err)
	}
}

func TestJobHelperProcess(t *testing.T) {
	switch os.Getenv("JEVCANCODE_JOB_HELPER") {
	case "parent":
		if err := ProtectApp(); err != nil {
			os.Exit(21)
		}
		exe, err := os.Executable()
		if err != nil {
			os.Exit(22)
		}
		child := exec.Command(exe, "-test.run=^TestJobHelperProcess$")
		child.Env = []string{"JEVCANCODE_JOB_HELPER=child"}
		// 별도 Group 없이도 부모의 보호 Job을 생성 순간부터 상속해야 한다.
		if err = child.Start(); err != nil {
			os.Exit(23)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	}
}
