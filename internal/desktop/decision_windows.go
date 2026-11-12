//go:build windows

package desktop

import (
	"fmt"
	"jevcancode/internal/application"
	"strings"
	"unicode/utf16"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

type publicTestsModel struct {
	walk.TableModelBase
	rows []map[string]any
}

func (m *publicTestsModel) RowCount() int { return len(m.rows) }
func (m *publicTestsModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	if col == 0 {
		return fmt.Sprint(row + 1)
	}
	if col == 1 {
		return field(m.rows[row], "inputs")
	}
	return field(m.rows[row], "want")
}
func (m *publicTestsModel) update(v any) {
	m.rows = nil
	if tests, ok := displayValue(v).([]any); ok {
		for _, test := range tests {
			if row, ok := test.(map[string]any); ok {
				m.rows = append(m.rows, row)
			}
		}
	}
	m.PublishRowsReset()
}

type candidateTable struct {
	walk.TableModelBase
	rows   []application.CandidateItem
	chosen string
}

func (m *candidateTable) StyleCell(style *walk.CellStyle) {
	if style.Row() < 0 || style.Row() >= len(m.rows) {
		return
	}
	c := m.rows[style.Row()]
	if c.ID == m.chosen && m.chosen != "" {
		style.BackgroundColor = walk.RGB(226, 244, 233)
		style.TextColor = walk.RGB(20, 105, 62)
	} else if c.Reason == "body_rejected" || c.Reason == "declaration_rejected" {
		style.BackgroundColor = walk.RGB(255, 241, 237)
		style.TextColor = walk.RGB(142, 57, 38)
	} else if c.Reason == "active" {
		style.BackgroundColor = walk.RGB(232, 241, 255)
		style.TextColor = walk.RGB(33, 83, 145)
	}
}
func (m *candidateTable) RowCount() int { return len(m.rows) }
func (m *candidateTable) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	c := m.rows[row]
	switch col {
	case 0:
		return c.Lexeme
	case 1:
		return c.ParentID
	case 2:
		if c.Score != nil {
			return fmt.Sprintf("%.4f", *c.Score)
		}
		return "—"
	case 3:
		if c.ID == m.chosen && m.chosen != "" {
			return "● 선택"
		}
		return ""
	case 4:
		switch c.Reason {
		case "active":
			return "유지"
		case "public_passed":
			return "테스트 통과"
		case "public_failed":
			return "테스트 실패"
		case "pruned", "pruned_strict", "duplicate":
			return "제외"
		case "body_rejected", "declaration_rejected":
			return rejectionDetail(c)
		case "forced":
			return "자동 확정"
		case "ranked":
			return "순위 산정"
		default:
			return "대기"
		}
	}
	return ""
}
func (m *candidateTable) update(rows []application.CandidateItem, chosen string) {
	m.rows = rows
	m.chosen = chosen
	m.PublishRowsReset()
}

func (w *window) searchPage() TabPage {
	cols := []TableViewColumn{{Title: "토큰", Width: 100}, {Title: "부모 경로", Width: 115}, {Title: "모델 점수", Width: 100}, {Title: "모델 선택", Width: 95}, {Title: "탐색 처리 / 제외 사유", Width: 200}}
	pathPages := make([]TabPage, 3)
	for i := range pathPages {
		n := i
		pathPages[i] = TabPage{AssignTo: &w.pathPages[i], Title: fmt.Sprintf("경로 %d", i+1), Layout: VBox{}, Children: []Widget{TabWidget{AssignTo: &w.codeTabs[i], OnCurrentIndexChanged: func() {
			if !w.updatingRows && w.codeTabs[n].CurrentIndex() == 1 {
				w.userBrowsing = true
			}
		}, Pages: []TabPage{
			{Title: "실시간 유지 코드", Layout: VBox{}, Children: []Widget{Composite{MaxSize: Size{Height: 34}, Layout: HBox{}, Children: []Widget{Label{Text: "탐색기가 유지한 코드"}, HSpacer{}, PushButton{Text: "코드 복사", OnClicked: func() { w.copyActual(n) }}}}, TextEdit{AssignTo: &w.actualCode[i], ReadOnly: true, VScroll: true, HScroll: true, Font: Font{Family: "Consolas", PointSize: 16}}}},
			{Title: "후보 미리보기", Layout: VBox{}, Children: []Widget{Composite{MaxSize: Size{Height: 34}, Layout: HBox{}, Children: []Widget{Label{AssignTo: &w.previewMarker[i], Text: "추가 위치: 선택된 후보 없음", Font: Font{Family: "맑은 고딕", PointSize: 11, Bold: true}, TextColor: walk.RGB(28, 91, 180)}, HSpacer{}, PushButton{Text: "원문 복사", OnClicked: func() { w.copyParent(n) }}}}, TextEdit{AssignTo: &w.pathCode[i], ReadOnly: true, VScroll: true, HScroll: true, Font: Font{Family: "Consolas", PointSize: 16}}}},
		}}}}
	}
	return TabPage{Title: "탐색", Layout: VBox{}, Children: []Widget{
		Composite{Layout: HBox{}, Children: []Widget{Label{Text: "모델"}, ComboBox{AssignTo: &w.model, MinSize: Size{Width: 150}}, Label{Text: "문제"}, ComboBox{AssignTo: &w.problem, MinSize: Size{Width: 180}}, Label{Text: "엔진"}, ComboBox{AssignTo: &w.engine, Model: []string{"token", "ast"}, CurrentIndex: 0, OnCurrentIndexChanged: w.updateStrategies}, Label{Text: "전략"}, ComboBox{AssignTo: &w.strategy, Model: strategiesForEngine(0), CurrentIndex: 0}, HSpacer{}}}, Composite{Layout: HBox{}, Children: []Widget{PushButton{Text: "세부 설정 ▾", OnClicked: func() { w.budgetPanel.SetVisible(!w.budgetPanel.Visible()) }}, PushButton{Text: "탐색 시작", OnClicked: func() { w.start("search") }}, PushButton{Text: "중지", OnClicked: w.cancel}, HSpacer{}, Label{AssignTo: &w.status, Text: "준비"}}},
		Composite{AssignTo: &w.budgetPanel, Layout: Grid{Columns: 8}, Children: []Widget{Label{Text: "최대 토큰"}, NumberEdit{AssignTo: &w.tokens, MinValue: 0, MaxValue: 100000, Value: 256.0}, Label{Text: "최대 호출"}, NumberEdit{AssignTo: &w.calls, MinValue: 0, MaxValue: 100000, Value: 128.0}, Label{Text: "최대 후보"}, NumberEdit{AssignTo: &w.candidates, MinValue: 0, MaxValue: 1000000, Value: 8192.0}, Label{Text: "제한 시간 (초)"}, NumberEdit{AssignTo: &w.timeout, MinValue: 0, MaxValue: 86400, Value: 300.0}, Label{Text: "지역 변수"}, NumberEdit{AssignTo: &w.locals, MinValue: 0, MaxValue: 1000, Value: 8.0}, Label{Text: "문장"}, NumberEdit{AssignTo: &w.statements, MinValue: 0, MaxValue: 10000, Value: 32.0}, Label{Text: "중첩"}, NumberEdit{AssignTo: &w.nesting, MinValue: 0, MaxValue: 100, Value: 4.0}, Label{Text: "AST 깊이"}, NumberEdit{AssignTo: &w.depth, MinValue: 0, MaxValue: 100, Value: 4.0}}},
		HSplitter{HandleWidth: 7, Children: []Widget{
			Composite{AssignTo: &w.historyPanel, StretchFactor: 15, MinSize: Size{Width: 170}, Layout: VBox{}, Children: []Widget{Label{Text: "판단 이력", Font: Font{Family: "맑은 고딕", PointSize: 12, Bold: true}}, Composite{MaxSize: Size{Height: 32}, Layout: HBox{}, Children: []Widget{PushButton{MinSize: Size{Width: 35}, MaxSize: Size{Width: 40}, Text: "◀", OnClicked: func() { w.moveReplay(-1) }}, PushButton{MinSize: Size{Width: 35}, MaxSize: Size{Width: 40}, Text: "▶", OnClicked: func() { w.moveReplay(1) }}, PushButton{MinSize: Size{Width: 35}, Text: "화면 정지", OnClicked: func() { w.replay.Pause(); w.playing = false }}}}, Composite{MaxSize: Size{Height: 32}, Layout: HBox{}, Children: []Widget{PushButton{MinSize: Size{Width: 35}, Text: "재생", OnClicked: func() { w.playing = !w.playing }}, PushButton{MinSize: Size{Width: 35}, Text: "실시간", OnClicked: w.live}}}, CheckBox{AssignTo: &w.rawEvents, Text: "개별 이벤트 보기", OnCheckedChanged: func() { w.refreshFrames() }}, ListBox{AssignTo: &w.replayList, OnCurrentIndexChanged: w.selectFrame}}},
			Composite{StretchFactor: 55, MinSize: Size{Width: 390}, Layout: VBox{}, Children: []Widget{
				Composite{Layout: HBox{}, Children: []Widget{Label{Text: "코드", Font: Font{Family: "맑은 고딕", PointSize: 12, Bold: true}}, PushButton{MinSize: Size{Width: 35}, MaxSize: Size{Width: 40}, Text: "A−", OnClicked: func() { w.changeCodeFont(-1) }}, PushButton{MinSize: Size{Width: 35}, MaxSize: Size{Width: 40}, Text: "A+", OnClicked: func() { w.changeCodeFont(1) }}, CheckBox{AssignTo: &w.followCode, Text: "자동 스크롤", Checked: true, OnCheckedChanged: func() {
					if w.followCode.Checked() {
						w.userBrowsing = false
					}
				}}, PushButton{AssignTo: &w.candidateToggle, Text: "후보 접기", OnClicked: func() {
					w.candidatesCollapsed = !w.candidatesCollapsed
					if w.candidatesCollapsed {
						_ = w.candidateToggle.SetText("후보 열기")
					} else {
						_ = w.candidateToggle.SetText("후보 접기")
					}
					if !w.codeFocus {
						w.candidatePanel.SetVisible(!w.candidatesCollapsed)
					}
				}}, PushButton{AssignTo: &w.focusButton, Text: "코드 집중", OnClicked: func() { w.setCodeFocus(!w.codeFocus) }}, HSpacer{}, CheckBox{AssignTo: &w.rawCode, Text: "원문", OnCheckedChanged: func() { w.showPreview() }}}}, Label{Visible: false, Text: "코드와 후보 토큰", Font: Font{Family: "맑은 고딕", PointSize: 12, Bold: true}},
				VSplitter{HandleWidth: 7, Children: []Widget{
					Composite{StretchFactor: 75, Layout: VBox{}, Children: []Widget{TabWidget{AssignTo: &w.pathTabs, Pages: pathPages, OnCurrentIndexChanged: func() {
						if !w.updatingRows {
							w.userBrowsing = true
							w.showPreview()
						}
					}}}},
					Composite{AssignTo: &w.candidatePanel, StretchFactor: 25, Layout: VBox{}, Children: []Widget{Composite{Layout: HBox{}, Children: []Widget{Label{AssignTo: &w.countLabel, Text: "전송 후보 0"}, HSpacer{}, Label{Text: "부모"}, ComboBox{AssignTo: &w.parentFilter, Model: []string{"전체"}, CurrentIndex: 0, MinSize: Size{Width: 110}, OnCurrentIndexChanged: func() {
						if !w.updatingParent {
							idx := w.parentFilter.CurrentIndex()
							w.selectedParent = ""
							if idx > 0 && idx <= len(w.parentIDs) {
								w.selectedParent = w.parentIDs[idx-1]
							}
							w.showFrame(w.decisionFrame)
						}
					}}, Label{Text: "검색"}, LineEdit{AssignTo: &w.filter, MinSize: Size{Width: 120}, OnTextChanged: func() { w.showFrame(w.decisionFrame) }}}},
						TabWidget{Pages: []TabPage{
							{AssignTo: &w.sentPage, Title: "모델 요청 후보", Layout: VBox{}, Children: []Widget{TableView{AssignTo: &w.sentTable, Model: w.sentModel, Columns: cols, AlternatingRowBG: true, CustomRowHeight: 26, NotSortableByHeaderClick: true, LastColumnStretched: true, OnCurrentIndexChanged: func() { w.pickRow(w.sentTable, w.sentModel) }}}},
							{AssignTo: &w.rejectedPage, Title: "검사에서 제외", Layout: VBox{}, Children: []Widget{TableView{AssignTo: &w.rejectedTable, Model: w.rejectedModel, Columns: cols, AlternatingRowBG: true, CustomRowHeight: 26, NotSortableByHeaderClick: true, LastColumnStretched: true, OnCurrentIndexChanged: func() { w.pickRow(w.rejectedTable, w.rejectedModel) }}}},
						}}}},
				}}}},
			Composite{AssignTo: &w.contextPanel, StretchFactor: 30, MinSize: Size{Width: 260}, Layout: VBox{}, Children: []Widget{Label{Text: "모델 판단", Font: Font{Family: "맑은 고딕", PointSize: 12, Bold: true}}, Label{AssignTo: &w.choiceHeadline, Text: "—", Font: Font{Family: "Consolas", PointSize: 22, Bold: true}, MaxSize: Size{Width: 360}, EllipsisMode: EllipsisEnd, TextColor: walk.RGB(20, 105, 62)}, Label{AssignTo: &w.choiceText, Text: "아직 판단 없음", Font: Font{Family: "맑은 고딕", PointSize: 10}}, TabWidget{AssignTo: &w.resultTabs, Pages: []TabPage{
				{Title: "문제와 테스트", Layout: VBox{}, Children: []Widget{TextEdit{AssignTo: &w.problemText, ReadOnly: true, VScroll: true, MaxSize: Size{Height: 160}}, Label{Text: "공개 테스트", Font: Font{Family: "맑은 고딕", PointSize: 11, Bold: true}}, TableView{AssignTo: &w.testsTable, Model: w.testsModel, Columns: []TableViewColumn{{Title: "#", Width: 40}, {Title: "입력", Width: 150}, {Title: "기대 결과", Width: 130}}, AlternatingRowBG: true, CustomRowHeight: 26, LastColumnStretched: true}}},
				{Title: "판단 문맥", Layout: VBox{}, Children: []Widget{TextEdit{AssignTo: &w.contextText, ReadOnly: true, VScroll: true, HScroll: true}}},
				{Title: "요청 상세", Layout: VBox{}, Children: []Widget{PushButton{Text: "내용 복사", OnClicked: func() { _ = walk.Clipboard().SetText(w.detailsText.Text()) }}, TextEdit{AssignTo: &w.detailsText, ReadOnly: true, VScroll: true, HScroll: true}}},
				{Title: "실행 결과", Layout: VBox{}, Children: []Widget{TextEdit{AssignTo: &w.resultText, ReadOnly: true, VScroll: true}}},
			}}}},
		}},
	}}
}

func (w *window) copyParent(index int) {
	if index < 0 || index >= len(w.pathIDs) {
		return
	}
	var s application.Snapshot
	if decodeDisplayJSON([]byte(w.decisionFrame.Text), &s) != nil {
		return
	}
	parents, _ := s.DecisionContext["parents"].(map[string]any)
	p, _ := parents[w.pathIDs[index]].(map[string]any)
	prefix, _ := p["prefix"].(string)
	if parents == nil && index == 0 {
		for _, c := range s.Candidates {
			if c.ID == w.selectedCandidate && c.Code != "" {
				prefix = c.Code
				break
			}
		}
	}
	if prefix == "" {
		prefix = recordedCode(s, w.pathIDs[index])
	}
	if prefix != "" {
		_ = walk.Clipboard().SetText(prefix)
	}
}
func (w *window) pickRow(tv *walk.TableView, m *candidateTable) {
	if w.updatingRows || tv == nil {
		return
	}
	i := tv.CurrentIndex()
	if i < 0 || i >= len(m.rows) {
		return
	}
	w.userBrowsing = true
	w.selectedCandidate = m.rows[i].ID
	w.previewSelectionPending = true
	if len(w.decisionFrame.Text) > 0 {
		var snapshot application.Snapshot
		_ = decodeDisplayJSON([]byte(w.decisionFrame.Text), &snapshot)
		if _, known := snapshot.DecisionContext["parents"].(map[string]any); !known {
			_ = w.pathTabs.SetCurrentIndex(0)
		}
	}
	for n, id := range w.pathIDs {
		if id == m.rows[i].ParentID || id == m.rows[i].ID {
			w.updatingRows = true
			_ = w.pathTabs.SetCurrentIndex(n)
			w.updatingRows = false
			_ = w.codeTabs[n].SetCurrentIndex(1)
			break
		}
	}
	w.showPreview()
}
func (w *window) showPreview() {
	var s application.Snapshot
	if decodeDisplayJSON([]byte(w.decisionFrame.Text), &s) != nil {
		return
	}
	parents, _ := s.DecisionContext["parents"].(map[string]any)
	for i := 0; i < 3; i++ {
		if i >= len(w.pathIDs) {
			_ = w.previewMarker[i].SetText("추가 위치: 경로 기록 없음")
			_ = w.pathCode[i].SetText("기록된 부모 경로 없음")
			_ = w.actualCode[i].SetText("기록된 유지 코드 없음")
			continue
		}
		p, _ := parents[w.pathIDs[i]].(map[string]any)
		prefix, _ := p["prefix"].(string)
		if parents == nil && i == 0 {
			for _, c := range s.Candidates {
				if c.ID == w.selectedCandidate && c.Code != "" {
					prefix = c.Code
					break
				}
			}
		}
		if prefix == "" {
			prefix = recordedCode(s, w.pathIDs[i])
		}
		actual := keptCode(s, w.pathIDs[i], w.selectedCandidate)
		if actual == "" {
			actual = "이 경로에서 유지된 코드 기록 없음"
		}
		if w.rawCode != nil && !w.rawCode.Checked() {
			actual, _, _ = previewCode(actual, "")
		}
		w.setCodeText(w.actualCode[i], actual, false, 0, 0)
		lexeme := ""
		for _, c := range s.Candidates {
			if c.ID == w.selectedCandidate && c.ParentID == w.pathIDs[i] && p != nil {
				lexeme = c.Lexeme
				break
			}
		}
		marker := "추가 위치: 선택된 후보 없음"
		if lexeme != "" {
			marker = "추가 위치: 코드 끝 → " + lexeme
		} else if parents == nil && prefix != "" {
			marker = "선택 후보의 기록된 코드"
		}
		_ = w.previewMarker[i].SetText(marker)
		text, start, end := previewCode(prefix, lexeme)
		if w.rawCode != nil && w.rawCode.Checked() {
			text = prefix
			if lexeme != "" {
				if text != "" {
					text += " "
				}
				start = len(utf16.Encode([]rune(text)))
				text += lexeme
				end = start + len(utf16.Encode([]rune(lexeme)))
			}
		}
		w.setCodeText(w.pathCode[i], text, w.previewSelectionPending && lexeme != "", start, end)
	}
	w.previewSelectionPending = false
}
func (w *window) showDecision(f Frame, s application.Snapshot) {
	oldPath := ""
	oldIndex := w.pathTabs.CurrentIndex()
	if oldIndex >= 0 && oldIndex < len(w.pathIDs) {
		oldPath = w.pathIDs[oldIndex]
	}
	w.decisionFrame = f
	parents, _ := s.DecisionContext["parents"].(map[string]any)
	ids := keys(parents)
	if len(ids) == 0 {
		for _, path := range s.Frontier {
			ids = append(ids, path.ID)
		}
		if len(ids) == 0 {
			for _, c := range s.Candidates {
				if c.Code != "" {
					ids = append(ids, c.ID)
					if len(ids) == 3 {
						break
					}
				}
			}
		}
	}
	w.pathIDs = ids
	if len(w.pathIDs) > 3 {
		w.pathIDs = w.pathIDs[:3]
	}
	w.updatingRows = true
	pathIndex := -1
	for i, id := range w.pathIDs {
		if id == oldPath && oldPath != "" {
			pathIndex = i
			break
		}
	}
	currentHasKept := false
	if pathIndex >= 0 {
		for _, p := range s.Frontier {
			if (p.ParentID == w.pathIDs[pathIndex] || p.ID == w.pathIDs[pathIndex]) && p.Code != "" {
				currentHasKept = true
				break
			}
		}
	}
	autoFollow := w.followCode != nil && w.followCode.Checked() && !w.userBrowsing
	if pathIndex < 0 || (autoFollow && !currentHasKept) {
		pathIndex = -1
		for _, path := range s.Frontier {
			for i, id := range w.pathIDs {
				if (path.ParentID == id || path.ID == id) && path.Code != "" {
					pathIndex = i
					break
				}
			}
			if pathIndex >= 0 {
				break
			}
		}
	}
	if pathIndex >= 0 {
		_ = w.pathTabs.SetCurrentIndex(pathIndex)
	}
	for i := 0; i < 3; i++ {
		title := fmt.Sprintf("경로 %d", i+1)
		if i < len(w.pathIDs) {
			title += " · " + w.pathIDs[i]
		}
		_ = w.pathPages[i].SetTitle(title)
	}
	w.updatingParent = true
	w.parentIDs = ids
	_, parentsKnown := s.DecisionContext["parents"].(map[string]any)
	w.parentFilter.SetEnabled(parentsKnown)
	if !parentsKnown {
		w.selectedParent = ""
	}
	choices := append([]string{"전체"}, ids...)
	idx := ParentIndex(ids, w.selectedParent)
	if idx == 0 {
		w.selectedParent = ""
	}
	_ = w.parentFilter.SetModel(choices)
	_ = w.parentFilter.SetCurrentIndex(idx)
	w.updatingParent = false
	chosen, status := snapshotDecision(f.Text)
	sent, rejected := decisionRows(s, chosen, w.selectedParent, w.filter.Text())
	if _, known := s.DecisionContext["criteria"].(map[string]any); !known {
		for _, c := range s.Candidates {
			if c.Reason != "body_rejected" && c.Reason != "declaration_rejected" && (w.selectedParent == "" || c.ParentID == w.selectedParent) && strings.Contains(strings.ToLower(c.Lexeme+" "+c.ID), strings.ToLower(w.filter.Text())) {
				sent = append(sent, c)
			}
		}
		_ = w.sentPage.SetTitle("전달 여부 기록 없음")
	} else {
		title := "기록된 요청 후보"
		switch status {
		case "prepared":
			title = "요청 준비 후보"
		case "calling":
			title = "요청 중 후보"
		case "received":
			title = "모델 요청 후보"
		case "failed":
			title = "요청 실패 후보"
		}
		_ = w.sentPage.SetTitle(title)
	}
	w.sentModel.update(sent, chosen)
	w.rejectedModel.update(rejected, chosen)
	_ = w.rejectedPage.SetTitle(fmt.Sprintf("검사에서 제외 (%d)", len(rejected)))
	allSent, allRejected := decisionRows(s, chosen, "", "")
	if _, known := s.DecisionContext["criteria"].(map[string]any); !known {
		for _, c := range s.Candidates {
			if c.Reason != "body_rejected" && c.Reason != "declaration_rejected" {
				allSent = append(allSent, c)
			}
		}
	}
	countLabel := fmt.Sprintf("요청 후보 %d/%d · 제외 %d/%d", len(sent), len(allSent), len(rejected), len(allRejected))
	if _, known := s.DecisionContext["criteria"].(map[string]any); !known {
		countLabel = fmt.Sprintf("미확인 %d/%d · 제외 %d/%d", len(sent), len(allSent), len(rejected), len(allRejected))
	}
	_ = w.countLabel.SetText(countLabel)
	selectedExists := false
	for _, c := range append(append([]application.CandidateItem{}, sent...), rejected...) {
		if c.ID == w.selectedCandidate {
			selectedExists = true
			break
		}
	}
	if !selectedExists {
		w.selectedCandidate = chosen
	}
	if w.selectedCandidate == "" && len(sent) > 0 {
		w.selectedCandidate = sent[0].ID
	}
	for i, c := range sent {
		if c.ID == w.selectedCandidate {
			_ = w.sentTable.SetCurrentIndex(i)
			break
		}
	}
	w.updatingRows = false
	summary := decisionSummary(s, chosen, status)

	_ = w.choiceHeadline.SetText(decisionHeadline(s, chosen, status))
	_ = w.choiceText.SetText(summary)
	if problem := displayProblem(s); problem != nil {
		_ = w.problemText.SetText("요구사항\r\n" + problem.Intent)
		w.testsModel.update(problem.PublicTests)
	} else {
		_ = w.problemText.SetText("문제 정보를 사용할 수 없습니다.")
		w.testsModel.update(nil)
	}
	_ = w.contextText.SetText(formatCompactContext(s.DecisionContext))
	_ = w.detailsText.SetText(formatDecisionContext(s.DecisionContext))
	_ = w.resultText.SetText(formatResult(s))
	w.showPreview()
}
