package jsoninput

import (
	"bufio"
	"bytes"
	"io"
)

// 2026-11-05: PowerShell이 붙인 파일 시작의 UTF-8 BOM만 제거하고 나머지 JSON 바이트는 보존한다.
func Reader(r io.Reader) io.Reader {
	buffered := bufio.NewReader(r)
	prefix, _ := buffered.Peek(3)
	if bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffered.Discard(3)
	}
	return buffered
}
