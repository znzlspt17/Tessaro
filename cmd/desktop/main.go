//go:build windows

package main

import (
	"github.com/lxn/walk"
	"jevcancode/internal/desktop"
	"jevcancode/internal/process"
)

func main() {
	if err := process.ProtectApp(); err != nil {
		walk.MsgBox(nil, "JevCanCode", safeErrorMessage(true, err), walk.MsgBoxIconError)
		return
	}
	if err := desktop.Run(); err != nil {
		walk.MsgBox(nil, "JevCanCode", safeErrorMessage(false, err), walk.MsgBoxIconError)
	}
}
