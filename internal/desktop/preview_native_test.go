//go:build windows

package desktop

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/lxn/walk"
	"github.com/lxn/win"
	"tessaro/internal/application"
	"runtime"
	"strings"
	"testing"
	"time"
)

var previewFixture = flag.Bool("native-preview", false, "show deterministic decision preview")

func TestNativeDecisionPreview(t *testing.T) {
	if !*previewFixture {
		t.Skip("interactive preview is opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := newWindow(desktopSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { time.Sleep(500 * time.Millisecond); w.mw.Dispose() }()
	populateDecisionFixture(w)
	w.mw.Run()
}

func populateDecisionFixture(w *window) {
	scoreA, scoreB := 0.62, 0.25
	longCode := strings.Repeat("value = append ( value , xs [ 0 ] ) ; ", 30)
	s := application.Snapshot{RunID: "fixture", Seq: 1, Status: "running", Model: "Jev", Step: 42, ChosenID: "b", DecisionStatus: "received", Current: &application.PublicProblem{ID: "reverse", Label: "배열 뒤집기", Intent: "정수 배열을 역순의 새 배열로 반환한다.", PublicTests: []any{map[string]any{"inputs": map[string]any{"xs": []int{1, 2, 3}}, "want": []int{3, 2, 1}}, map[string]any{"inputs": map[string]any{"xs": []int{}}, "want": []int{}}}}, DecisionContext: map[string]any{"intent": "정수 배열을 역순의 새 배열로 반환한다.", "inputs": []any{"xs"}, "type_hints": map[string]any{"xs": "[]int"}, "output_type_hint": "[]int", "parents": map[string]any{"p1": map[string]any{"prefix": "package main ; func f0 ( xs [ ] int ) [ ] int { return", "visible_symbols": map[string]any{"xs": true}}, "p2": map[string]any{"prefix": "package main ; func f0 ( xs [ ] int ) [ ] int { make ( " + longCode, "visible_symbols": map[string]any{"xs": true}}}, "criteria": map[string]any{"a": map[string]any{"parent_id": "p1", "lexeme": "xs"}, "b": map[string]any{"parent_id": "p2", "lexeme": "["}}, "instructions": "Choose a token."}, Candidates: []application.CandidateItem{{ID: "a", ParentID: "p1", Lexeme: "xs", Rank: 2, Score: &scoreB, Reason: "pruned"}, {ID: "b", ParentID: "p2", Lexeme: "[", Rank: 1, Score: &scoreA, Reason: "active"}, {ID: "c", ParentID: "p1", Lexeme: ";", Reason: "body_rejected", RejectionReason: "unused_expression"}}, Frontier: []application.FrontierItem{{ID: "b", ParentID: "p2", Rank: 1, Code: "package main ; func f0 ( xs [ ] int ) [ ] int { make ( " + longCode + " ["}}}
	data, _ := json.Marshal(s)
	f := Frame{RunID: "fixture", Seq: 1, Group: "1:42", Text: string(data)}
	w.frames = []Frame{f}
	w.replay.Add(f)
	w.refreshFrames()
	w.showFrame(f)
}

func TestNativeDecisionControls(t *testing.T) {
	if !*nativeGUI {
		t.Skip("requires native manifest")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := newWindow(desktopSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { time.Sleep(500 * time.Millisecond); w.mw.Dispose() }()
	populateDecisionFixture(w)
	if w.fontSize != 16 || w.followCode == nil || !w.followCode.Checked() || w.codeTabs[0] == nil || w.codeTabs[0].CurrentIndex() != 0 {
		t.Fatal("default actual editor/font/follow mode missing")
	}
	w.changeCodeFont(100)
	if w.fontSize != 24 {
		t.Fatal("font upper limit")
	}
	w.changeCodeFont(-100)
	if w.fontSize != 12 {
		t.Fatal("font lower limit")
	}
	w.changeCodeFont(4)
	w.setCodeFocus(true)
	if !w.codeFocus || w.historyPanel.Visible() || w.contextPanel.Visible() || w.candidatePanel.Visible() {
		t.Fatal("code focus did not hide panels")
	}
	w.setCodeFocus(false)
	if w.codeFocus || !w.historyPanel.Visible() || !w.contextPanel.Visible() || !w.candidatePanel.Visible() {
		t.Fatal("code focus did not restore panels")
	}
	for i, code := range w.pathCode {
		if win.GetWindowLong(code.Handle(), win.GWL_STYLE)&win.ES_NOHIDESEL == 0 {
			t.Fatalf("path %d does not retain token highlight when focus changes", i)
		}
	}

	if w.sentModel.RowCount() != 2 || w.rejectedModel.RowCount() != 1 || w.testsModel.RowCount() != 2 {
		t.Fatalf("rows: %d %d %d", w.sentModel.RowCount(), w.rejectedModel.RowCount(), w.testsModel.RowCount())
	}
	if w.selectedCandidate != "b" || w.pathTabs.CurrentIndex() != 1 || !strings.Contains(w.actualCode[1].Text(), "make (") {
		t.Fatalf("initial candidate=%s path=%d actual=%q", w.selectedCandidate, w.pathTabs.CurrentIndex(), w.actualCode[1].Text())
	}
	if err := w.pathTabs.SetCurrentIndex(0); err != nil {
		t.Fatal(err)
	}
	w.followCode.SetChecked(false)
	w.showFrame(w.decisionFrame)
	if w.pathTabs.CurrentIndex() != 0 {
		t.Fatal("live update changed manually selected parent")
	}
	if err := w.sentTable.SetCurrentIndex(1); err != nil {
		t.Fatal(err)
	}
	w.pickRow(w.sentTable, w.sentModel)
	if w.selectedCandidate != "a" || w.pathTabs.CurrentIndex() != 0 || w.codeTabs[0].CurrentIndex() != 1 || !strings.Contains(w.pathCode[0].Text(), "xs") {
		t.Fatalf("selection candidate=%s path=%d code=%q", w.selectedCandidate, w.pathTabs.CurrentIndex(), w.pathCode[0].Text())
	}
	start, end := w.pathCode[0].TextSelection()
	if start == end {
		t.Fatalf("missing token selection: %d:%d, text=%q", start, end, w.pathCode[0].Text())
	}
	if !strings.Contains(w.previewMarker[0].Text(), "xs") {
		t.Fatalf("missing persistent token marker: %q", w.previewMarker[0].Text())
	}
	w.rawCode.SetChecked(true)
	w.showPreview()
	if !strings.Contains(w.pathCode[0].Text(), "package main ; func") {
		t.Fatal("raw code unavailable")
	}
	for _, size := range []walk.Size{{Width: 2100, Height: 900}, {Width: 1306, Height: 843}, {Width: 1100, Height: 720}} {
		if err := w.mw.SetBounds(walk.Rectangle{Width: size.Width, Height: size.Height}); err != nil {
			t.Fatal(err)
		}
		if w.mw.Bounds().Width < size.Width-10 || w.mw.Bounds().Width > size.Width+10 || (size.Height == 843 && w.mw.Bounds().Height > size.Height+10) {
			t.Fatalf("cannot resize: %+v", w.mw.Bounds())
		}
	}
}

func TestNativeLiveCodeScrollPreserved(t *testing.T) {
	if !*nativeGUI {
		t.Skip("requires native manifest")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := newWindow(desktopSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { time.Sleep(500 * time.Millisecond); w.mw.Dispose() }()
	populateDecisionFixture(w)
	results := make(chan string, 1)
	w.mw.Starting().Attach(func() {
		time.AfterFunc(800*time.Millisecond, func() {
			w.mw.Synchronize(func() {
				w.followCode.SetChecked(false)
				e := w.actualCode[1]
				line := strings.Repeat("long_source_", 45)
				text := strings.Repeat(line+"\r\n", 80)
				_ = e.SetText(text)
				e.SetTextSelection(2, 5)
				e.SendMessage(win.EM_LINESCROLL, 80, 25)
				beforeV := int(e.SendMessage(win.EM_GETFIRSTVISIBLELINE, 0, 0))
				beforeH := horizontalScroll(e)
				w.setCodeText(e, text+"another line", false, 0, 0)
				afterV := int(e.SendMessage(win.EM_GETFIRSTVISIBLELINE, 0, 0))
				afterH := horizontalScroll(e)
				a, b := e.TextSelection()
				if beforeV == 0 || beforeH == 0 {
					results <- fmt.Sprintf("could not establish scrolled state: v=%d h=%d", beforeV, beforeH)
				} else if beforeV != afterV || beforeH != afterH || a != 2 || b != 5 {
					results <- fmt.Sprintf("scroll or selection lost: v %d->%d h %d->%d selection %d:%d", beforeV, afterV, beforeH, afterH, a, b)
				} else {
					results <- ""
				}
				w.mw.Close()
			})
		})
	})
	w.mw.Run()
	if result := <-results; result != "" {
		t.Fatal(result)
	}
}
