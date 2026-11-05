package tokenlex

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"
)

// 2026-11-05: 선택지 하나가 완성 코드나 복수 토큰으로 바뀌는 것을 요청 경계에서 차단한다.
func Kind(value string) (token.Token, error) {
	if strings.TrimSpace(value) != value || value == "" {
		return token.ILLEGAL, fmt.Errorf("one lexical token required")
	}
	fset := token.NewFileSet()
	file := fset.AddFile("token", -1, len(value))
	var s scanner.Scanner
	errors := 0
	s.Init(file, []byte(value), func(token.Position, string) { errors++ }, scanner.ScanComments)
	kind := token.ILLEGAL
	count := 0
	for {
		_, k, lit := s.Scan()
		if k == token.EOF {
			break
		}
		if k == token.SEMICOLON && lit == "\n" {
			continue
		}
		if k == token.COMMENT || k == token.ILLEGAL {
			return token.ILLEGAL, fmt.Errorf("one lexical token required")
		}
		kind = k
		count++
	}
	if errors != 0 || count != 1 {
		return token.ILLEGAL, fmt.Errorf("one lexical token required")
	}
	return kind, nil
}
