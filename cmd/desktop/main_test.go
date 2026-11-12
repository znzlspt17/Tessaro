//go:build windows

package main

import (
	"errors"
	"jevcancode/internal/desktop"
	"testing"
)

func TestSafeErrorMessages(t *testing.T) {
	cases := []struct {
		name      string
		protected bool
		err       error
		want      string
	}{
		{"protection", true, errors.New("sensitive detail"), "[APP-PROTECT] 보호된 실행 환경을 준비할 수 없습니다."},
		{"window", false, &desktop.StageError{Stage: desktop.StageWindowCreate}, "[UI-CREATE] 앱 창을 만들 수 없습니다."},
		{"shutdown", false, &desktop.StageError{Stage: desktop.StageShutdown}, "[APP-SHUTDOWN] 앱을 안전하게 종료하지 못했습니다."},
		{"unknown", false, errors.New("sensitive detail"), "[APP-RUN] 앱을 실행할 수 없습니다."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := safeErrorMessage(c.protected, c.err); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}
