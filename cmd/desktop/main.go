//go:build windows

package main

import (
	"github.com/lxn/walk"
	"tessaro/internal/desktop"
	"tessaro/internal/process"
)

func main() {
	if err := process.ProtectApp(); err != nil {
		walk.MsgBox(nil, "Tessaro", safeErrorMessage(true, err), walk.MsgBoxIconError)
		return
	}
	if err := desktop.Run(); err != nil {
		walk.MsgBox(nil, "Tessaro", safeErrorMessage(false, err), walk.MsgBoxIconError)
	}
}
