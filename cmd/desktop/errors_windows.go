//go:build windows

package main

import (
	"errors"
	"tessaro/internal/desktop"
)

func safeErrorMessage(protection bool, err error) string {
	if protection {
		return "[APP-PROTECT] 보호된 실행 환경을 준비할 수 없습니다."
	}
	var stage *desktop.StageError
	if errors.As(err, &stage) {
		switch stage.Stage {
		case desktop.StageWindowCreate:
			return "[UI-CREATE] 앱 창을 만들 수 없습니다."
		case desktop.StageShutdown:
			return "[APP-SHUTDOWN] 앱을 안전하게 종료하지 못했습니다."
		}
	}
	return "[APP-RUN] 앱을 실행할 수 없습니다."
}
