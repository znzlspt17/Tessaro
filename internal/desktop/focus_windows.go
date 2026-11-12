//go:build windows

package desktop

import (
	"github.com/lxn/walk"
	"github.com/lxn/win"
	"tessaro/internal/application"
	"unicode/utf16"
	"unsafe"
)

// 2026-11-08: 집중 모드 종료 시 사용자가 접었던 후보 영역의 상태를 복원한다.
func (w *window) setCodeFocus(value bool) {
	w.codeFocus = value
	if value {
		_ = w.focusButton.SetText("집중 종료")
	} else {
		_ = w.focusButton.SetText("코드 집중")
	}
	w.historyPanel.SetVisible(!value)
	w.contextPanel.SetVisible(!value)
	w.candidatePanel.SetVisible(!value && !w.candidatesCollapsed)
}
func (w *window) changeCodeFont(delta int) {
	size := w.fontSize + delta
	if size < 12 {
		size = 12
	}
	if size > 24 {
		size = 24
	}
	font, err := walk.NewFont("Consolas", size, 0)
	if err != nil {
		return
	}
	w.fontSize = size
	for i := range w.pathCode {
		w.pathCode[i].SetFont(font)
		w.actualCode[i].SetFont(font)
	}
	win.RedrawWindow(w.mw.Handle(), nil, 0, win.RDW_INVALIDATE|win.RDW_ERASE|win.RDW_ALLCHILDREN)
}
func (w *window) copyActual(index int) {
	if index < 0 || index >= len(w.pathIDs) {
		return
	}
	var snapshot application.Snapshot
	if decodeDisplayJSON([]byte(w.decisionFrame.Text), &snapshot) != nil {
		return
	}
	parent := w.pathIDs[index]
	var first string
	for _, path := range snapshot.Frontier {
		if (path.ParentID == parent || path.ID == parent) && path.Code != "" {
			if path.ID == w.selectedCandidate {
				_ = walk.Clipboard().SetText(path.Code)
				return
			}
			if first == "" {
				first = path.Code
			}
		}
	}
	if first != "" {
		_ = walk.Clipboard().SetText(first)
	}
}

// 2026-11-08: 반복 갱신으로 사용자의 스크롤과 선택이 초기화되지 않도록 첫 줄 위치를 보존한다.
func (w *window) setCodeText(edit *walk.TextEdit, value string, mark bool, start, end int) {
	changed := edit.Text() != value
	if changed {
		first := int(edit.SendMessage(win.EM_GETFIRSTVISIBLELINE, 0, 0))
		horizontal := horizontalScroll(edit)
		a, b := edit.TextSelection()
		_ = edit.SetText(value)
		if w.followCode != nil && w.followCode.Checked() {
			pos := len(utf16.Encode([]rune(value)))
			edit.SetTextSelection(pos, pos)
			edit.ScrollToCaret()
		} else {
			edit.SetTextSelection(a, b)
			now := int(edit.SendMessage(win.EM_GETFIRSTVISIBLELINE, 0, 0))
			edit.SendMessage(win.EM_LINESCROLL, 0, uintptr(first-now))
			edit.SendMessage(win.WM_HSCROLL, uintptr(uint32(win.SB_THUMBPOSITION)|uint32(horizontal)<<16), 0)
		}
	}
	if mark {
		edit.SetTextSelection(start, end)
		if w.followCode != nil && w.followCode.Checked() {
			edit.ScrollToCaret()
		}
	}
}
func horizontalScroll(edit *walk.TextEdit) int {
	info := win.SCROLLINFO{CbSize: uint32(unsafe.Sizeof(win.SCROLLINFO{})), FMask: win.SIF_POS}
	if !win.GetScrollInfo(edit.Handle(), win.SB_HORZ, &info) {
		return 0
	}
	return int(info.NPos)
}
