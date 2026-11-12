package verify

import (
	"os"
	"path/filepath"
)

func commonEnv(dir string) []string {
	env := []string{"TEMP=" + dir, "TMP=" + dir}
	for _, name := range []string{"SystemRoot", "WINDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// 2026-11-05: 자식에게 부모 환경 전체가 전달되지 않도록 빌드와 실행 환경을 별도로 구성한다.
func compileEnv(dir string) []string {
	env := commonEnv(dir)
	return append(env, "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GO111MODULE=off", "CGO_ENABLED=0", "GOCACHE="+filepath.Join(os.TempDir(), "jevcancode-go-cache"), "GOPATH="+filepath.Join(dir, "gopath"))
}
func executeEnv(dir string) []string { return commonEnv(dir) }
