package tokenverify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"tessaro/internal/process"
	"tessaro/internal/tokenspec"
	"tessaro/internal/verify"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func literal(v any, typ string) (string, error) {
	if !tokenspec.Matches(v, typ) {
		return "", fmt.Errorf("test value mismatch")
	}
	switch typ {
	case "int":
		switch x := v.(type) {
		case json.Number:
			n, err := strconv.ParseInt(string(x), 10, strconv.IntSize)
			if err != nil {
				return "", fmt.Errorf("invalid integer")
			}
			return strconv.FormatInt(n, 10), nil
		case int:
			return strconv.Itoa(x), nil
		}
	case "bool":
		return strconv.FormatBool(v.(bool)), nil
	case "string":
		return strconv.Quote(v.(string)), nil
	case "[]int", "[]string":
		items := v.([]any)
		values := make([]string, len(items))
		for i, item := range items {
			value, err := literal(item, strings.TrimPrefix(typ, "[]"))
			if err != nil {
				return "", err
			}
			values[i] = value
		}
		return typ + "{" + strings.Join(values, ",") + "}", nil
	case "map[string]int", "map[string]string":
		items := v.(map[string]any)
		keys := make([]string, 0, len(items))
		for k := range items {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		values := make([]string, len(keys))
		element := strings.TrimPrefix(typ, "map[string]")
		for i, k := range keys {
			value, err := literal(items[k], element)
			if err != nil {
				return "", err
			}
			values[i] = strconv.Quote(k) + ":" + value
		}
		return typ + "{" + strings.Join(values, ",") + "}", nil
	}
	return "", fmt.Errorf("unsupported test value")
}
func harness(p tokenspec.Problem, b *Binding, tests []tokenspec.Test) (string, error) {
	return makeHarness(p, b, tests, false)
}
func makeHarness(p tokenspec.Problem, b *Binding, tests []tokenspec.Test, counts bool) (string, error) {
	if len(tests) == 0 {
		return "", fmt.Errorf("tests required")
	}
	typesByName, outType, err := p.Contract()
	if err != nil {
		return "", err
	}
	refAlias := "_token_reflect"
	osAlias := "_token_os"
	for refAlias == b.Name || refAlias == osAlias {
		refAlias += "_"
	}
	for osAlias == b.Name || osAlias == refAlias {
		osAlias += "_"
	}
	fmtAlias := "_token_fmt"
	for fmtAlias == b.Name || fmtAlias == refAlias || fmtAlias == osAlias {
		fmtAlias += "_"
	}
	countName := "_token_passed"
	for countName == b.Name || countName == fmtAlias || countName == refAlias || countName == osAlias {
		countName += "_"
	}
	var src strings.Builder
	fmt.Fprintf(&src, "package main\nimport (%s \"reflect\"; %s \"os\"; %s \"fmt\")\nfunc main(){\n_ = %s.Print\n", refAlias, osAlias, fmtAlias, fmtAlias)
	if counts {
		fmt.Fprintf(&src, "%s := 0; _ = %s.Exit\n", countName, osAlias)
	}
	for _, test := range tests {
		if len(test.Inputs) != len(p.Inputs) {
			return "", fmt.Errorf("input names mismatch")
		}
		args := make([]string, len(b.Params))
		for i, name := range b.Params {
			value, ok := test.Inputs[name]
			if !ok {
				return "", fmt.Errorf("missing input")
			}
			args[i], err = literal(value, typesByName[name])
			if err != nil {
				return "", err
			}
		}
		want, err := literal(test.Want, outType)
		if err != nil {
			return "", err
		}
		if counts {
			fmt.Fprintf(&src, "if %s.DeepEqual(%s(%s), %s) { %s++ }\n", refAlias, b.Name, strings.Join(args, ","), want, countName)
		} else {
			fmt.Fprintf(&src, "if !%s.DeepEqual(%s(%s), %s) { %s.Exit(1) }\n", refAlias, b.Name, strings.Join(args, ","), want, osAlias)
		}
	}
	if counts {
		fmt.Fprintf(&src, "%s.Print(%s)\n", fmtAlias, countName)
	}
	src.WriteString("}\n")
	return src.String(), nil
}

// 2026-11-05: 검증된 제한 문법만 별도 환경에서 실행하며 컴파일과 실행을 각각 제한한다.
func Check(ctx context.Context, p tokenspec.Problem, source string, tests []tokenspec.Test) (bool, Timing, error) {
	return CheckObserved(ctx, p, source, tests, nil)
}

// 2026-11-05: 단계 알림은 실행을 관찰하며 요청·검증 횟수를 늘리지 않는다.
func CheckObserved(ctx context.Context, p tokenspec.Problem, source string, tests []tokenspec.Test, observe func(string, string)) (bool, Timing, error) {
	notify := func(phase, status string) {
		if observe != nil {
			observe(phase, status)
		}
	}
	var timing Timing
	if err := ctx.Err(); err != nil {
		return false, timing, err
	}
	binding, err := Validate(p, source)
	if err != nil {
		return false, timing, err
	}
	mainSource, err := harness(p, binding, tests)
	if err != nil {
		return false, timing, err
	}
	dir, err := os.MkdirTemp("", "tessaro-token-*")
	if err != nil {
		return false, timing, err
	}
	defer os.RemoveAll(dir)
	candidate := filepath.Join(dir, "candidate.go")
	main := filepath.Join(dir, "main.go")
	if err = os.WriteFile(candidate, []byte(binding.Source), 0600); err != nil {
		return false, timing, err
	}
	if err = os.WriteFile(main, []byte(mainSource), 0600); err != nil {
		return false, timing, err
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return false, timing, fmt.Errorf("go executable not found")
	}
	goPath, err = filepath.Abs(goPath)
	if err != nil {
		return false, timing, fmt.Errorf("invalid go executable path")
	}
	exe := filepath.Join(dir, "candidate.exe")
	compileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cmd := exec.CommandContext(compileCtx, goPath, "build", "-o", exe, candidate, main)
	cmd.Env = verify.ExportCompileEnv(dir)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	started := time.Now()
	notify("compile", "started")
	err = process.RunContext(compileCtx, cmd)
	timing.Compile = time.Since(started)
	compileErr := compileCtx.Err()
	cancel()
	if err != nil {
		notify("compile", "failed")
		if ctx.Err() != nil {
			return false, timing, ctx.Err()
		}
		if compileErr == context.DeadlineExceeded {
			return false, timing, ErrCompileTimeout
		}
		return false, timing, ErrCompileFailed
	}
	notify("compile", "passed")
	timing.Compiled = true
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	cmd = exec.CommandContext(runCtx, exe)
	cmd.Env = verify.ExportExecuteEnv(dir)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	started = time.Now()
	notify("execute", "started")
	err = process.RunContext(runCtx, cmd)
	timing.Execute = time.Since(started)
	runErr := runCtx.Err()
	cancel()
	if err != nil {
		notify("execute", "failed")
		if ctx.Err() != nil {
			return false, timing, ctx.Err()
		}
		if runErr == context.DeadlineExceeded {
			return false, timing, ErrExecutionTimeout
		}
		if _, ok := err.(*exec.ExitError); ok {
			return false, timing, nil
		}
		return false, timing, fmt.Errorf("candidate execution failed")
	}
	notify("execute", "passed")
	return true, timing, nil
}
