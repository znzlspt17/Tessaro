//go:build windows

package process

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

type Group struct{ job windows.Handle }

// 2026-11-07: 앱이 시작한 자식 트리만 종료하고 별도 콘솔 창을 열지 않는다.
func Start(cmd *exec.Cmd) (*Group, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	if err = resumeInitialThread(uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	return &Group{job: job}, nil
}
func (g *Group) Close() error {
	if g == nil || g.job == 0 {
		return nil
	}
	err := windows.CloseHandle(g.job)
	g.job = 0
	return err
}

// 2026-11-07: 실행 취소 시 Wait보다 먼저 자식 Job 전체를 닫아 하위 프로세스의 출력 파이프가 대기를 붙잡지 못하게 한다.
func RunContext(ctx context.Context, cmd *exec.Cmd) error {
	g, err := Start(cmd)
	if err != nil {
		return err
	}
	defer g.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		_ = g.Close()
		<-done
		return ctx.Err()
	}
}
func Run(cmd *exec.Cmd) error { return RunContext(context.Background(), cmd) }

// 2026-11-07: Job에 넣기 전에 사용자 코드가 실행되지 않도록 초기 스레드를 중지 상태에서 시작한다.
func resumeInitialThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err = windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == pid {
			thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if e != nil {
				return e
			}
			previous, e := windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			if e != nil {
				return e
			}
			if previous != 1 {
				return fmt.Errorf("invalid suspended process state")
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		if err = windows.Thread32Next(snapshot, &entry); err != nil {
			return fmt.Errorf("initial process thread unavailable")
		}
	}
}

var protectOnce sync.Once
var protectErr error
var applicationJob windows.Handle

// 2026-11-07: 앱 자신을 최상위 Job에 넣어 자식 생성과 별도 Job 연결 사이에도 고아를 방지한다.
// 이 핸들은 프로세스가 끝날 때 Windows가 닫으며 살아있는 자식 트리를 정리한다.
func ProtectApp() error {
	protectOnce.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			protectErr = err
			return
		}
		var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
			windows.CloseHandle(job)
			protectErr = err
			return
		}
		if err = windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
			windows.CloseHandle(job)
			protectErr = err
			return
		}
		applicationJob = job
	})
	return protectErr
}
