package jsoninput

import (
	"io"
	"strings"
	"testing"
)

func TestReaderPreservesEverythingExceptInitialBOM(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""},
		{"{}", "{}"},
		{"\xef\xbb\xbf{}", "{}"},
		{"\xef\xbb", "\xef\xbb"},
		{"\"\ufeff\"", "\"\ufeff\""},
		{"{} {}", "{} {}"},
		{"\xef\xbb\xbf\xef\xbb\xbf{}", "\xef\xbb\xbf{}"},
	} {
		data, err := io.ReadAll(Reader(strings.NewReader(tc.input)))
		if err != nil || string(data) != tc.want {
			t.Errorf("input=%q result=%q error=%v", tc.input, data, err)
		}
	}
}
