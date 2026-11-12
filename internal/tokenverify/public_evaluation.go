package tokenverify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"tessaro/internal/process"
	"tessaro/internal/tokenspec"
	"tessaro/internal/verify"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type PublicEvaluation struct {
	Status string `json:"status"`
	Passed int    `json:"passed"`
	Total  int    `json:"total"`
	Timing Timing `json:"timing"`
}

// 2026-11-12: 전체 공개 사례가 정상 종료된 경우에만 점수를 노출해 부분 실행을 보상하지 않는다.
func EvaluatePublic(ctx context.Context, p tokenspec.Problem, source string, tests []tokenspec.Test) (PublicEvaluation, error) {
	result := PublicEvaluation{Total: len(tests)}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := p.Validate(); err != nil {
		return result, err
	}
	binding, err := Validate(p, source)
	if err != nil {
		result.Status = "typecheck_failed"
		return result, nil
	}
	mainSource, err := makeHarness(p, binding, tests, true)
	if err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp("", "tessaro-public-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	candidate, main := filepath.Join(dir, "candidate.go"), filepath.Join(dir, "main.go")
	if err := os.WriteFile(candidate, []byte(binding.Source), 0600); err != nil {
		return result, err
	}
	if err := os.WriteFile(main, []byte(mainSource), 0600); err != nil {
		return result, err
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return result, err
	}
	goPath, err = filepath.Abs(goPath)
	if err != nil {
		return result, err
	}
	exe := filepath.Join(dir, "candidate.exe")
	compileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cmd := exec.CommandContext(compileCtx, goPath, "build", "-o", exe, candidate, main)
	cmd.Env = verify.ExportCompileEnv(dir)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	start := time.Now()
	err = process.RunContext(compileCtx, cmd)
	result.Timing.Compile = time.Since(start)
	timed := compileCtx.Err()
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(timed, context.DeadlineExceeded) {
			result.Status = "compile_timeout"
			return result, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// 2026-11-12: 타입 검사를 통과한 후보의 컴파일 실패는 환경 장애일 수 있어 보상으로 기록하지 않는다.
			result.Status = "compile_failed"
			return result, fmt.Errorf("public evaluation compiler failed")
		}
		return result, err
	}
	result.Timing.Compiled = true
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	cmd = exec.CommandContext(runCtx, exe)
	cmd.Env = verify.ExportExecuteEnv(dir)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	start = time.Now()
	err = process.RunContext(runCtx, cmd)
	result.Timing.Execute = time.Since(start)
	timed = runCtx.Err()
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(timed, context.DeadlineExceeded) {
			result.Status = "execution_timeout"
			return result, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.Status = "execution_failed"
			return result, nil
		}
		return result, err
	}
	passed, err := strconv.Atoi(output.buf.String())
	if err != nil || passed < 0 || passed > len(tests) || output.overflow {
		return result, fmt.Errorf("invalid public evaluation output")
	}
	result.Status = "completed"
	result.Passed = passed
	return result, nil
}

type boundedOutput struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.buf.Len()+n > 64 {
		b.overflow = true
		return n, nil
	}
	_, err := b.buf.Write(p)
	return n, err
}
