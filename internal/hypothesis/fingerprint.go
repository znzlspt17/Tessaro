package hypothesis

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// 2026-11-08: 실행 파일과 작업 트리의 고정된 소스 목록을 별도로 식별한다. 둘이 일치한다는 보장은 없다.
func fingerprintCode() (map[string]string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, errors.New("source fingerprint unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	exe, e := os.Executable()
	if e != nil {
		return nil, errors.New("executable fingerprint unavailable")
	}
	paths := map[string]string{"executable_sha256": exe}
	for _, name := range []string{"internal/tokensynth/search.go", "internal/tokensynth/frontend.go", "internal/tokensynth/bool_order.go", "internal/tokensynth/header.go", "internal/tokenlex/token.go", "internal/grammar/earley.go", "internal/grammar/grammar.go", "internal/tokenverify/verify.go", "internal/tokenverify/validate.go", "internal/tokenverify/declaration.go", "internal/tokenverify/body.go", "internal/tokenverify/check.go", "third_party/tree-sitter-go/grammar.json", "internal/rank/rank.go", "internal/rank/http.go", "internal/runner/token.go", "internal/hypothesis/hypothesis.go", "internal/hypothesis/evaluate.go", "internal/hypothesis/sampling.go", "internal/hypothesis/guard.go", "internal/hypothesis/metrics.go", "internal/hypothesis/generation.go"} {
		paths["source:"+name] = filepath.Join(root, filepath.FromSlash(name))
	}
	out := map[string]string{}
	for name, path := range paths {
		f, e := os.Open(path)
		if e != nil {
			return nil, errors.New("source fingerprint unavailable")
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return nil, errors.New("source fingerprint unavailable")
		}
		out[name] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}
