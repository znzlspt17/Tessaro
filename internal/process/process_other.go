//go:build !windows

package process

import (
	"context"
	"os/exec"
)

type Group struct{ cmd *exec.Cmd }

func Start(cmd *exec.Cmd) (*Group, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Group{cmd}, nil
}
func (g *Group) Close() error {
	if g == nil || g.cmd.Process == nil {
		return nil
	}
	return g.cmd.Process.Kill()
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

func ProtectApp() error { return nil }
