//go:build windows

package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"jevcancode/internal/application"
	"jevcancode/internal/researchresults"
)

type researchTable struct {
	walk.TableModelBase
	rows [][]interface{}
}

func (m *researchTable) RowCount() int { return len(m.rows) }
func (m *researchTable) Value(r, c int) interface{} {
	if r < 0 || r >= len(m.rows) || c < 0 || c >= len(m.rows[r]) {
		return ""
	}
	return m.rows[r][c]
}
func (m *researchTable) set(rows [][]interface{}) { m.rows = rows; m.PublishRowsReset() }

type researchView struct {
	path                             *walk.LineEdit
	split, strategy, problem, status *walk.ComboBox
	summary, trials                  *walk.TableView
	detail                           *walk.TextEdit
	label                            *walk.Label
	summaryModel, trialModel         researchTable
	study                            researchresults.Study
	rows                             []researchTrial
	generation                       uint64
	updating                         bool
}

func (w *window) researchPage(project string) TabPage {
	v := &researchView{}
	w.research = v
	return TabPage{Title: "실험 결과", Layout: VBox{}, Children: []Widget{
		Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &v.path, Text: filepath.Join(project, "output", "research-final")}, PushButton{Text: "폴더 선택", OnClicked: func() { w.chooseDirectory(v.path); w.loadResearch() }}, PushButton{Text: "불러오기", OnClicked: w.loadResearch}}},
		Label{AssignTo: &v.label, Text: "결과를 불러오는 중"},
		Composite{Layout: HBox{}, Children: []Widget{Label{Text: "구분"}, ComboBox{AssignTo: &v.split, Model: []string{"전체", "dev", "eval"}, CurrentIndex: 0, OnCurrentIndexChanged: w.refreshResearch}, Label{Text: "전략"}, ComboBox{AssignTo: &v.strategy, Model: []string{"전체"}, CurrentIndex: 0, OnCurrentIndexChanged: w.refreshResearch}, Label{Text: "문제"}, ComboBox{AssignTo: &v.problem, Model: []string{"전체"}, CurrentIndex: 0, OnCurrentIndexChanged: w.refreshResearch}, Label{Text: "결과"}, ComboBox{AssignTo: &v.status, Model: []string{"전체", "성공", "실패"}, CurrentIndex: 0, OnCurrentIndexChanged: w.refreshResearch}, HSpacer{}}},
		TableView{AssignTo: &v.summary, Model: &v.summaryModel, MinSize: Size{Height: 110}, Columns: []TableViewColumn{{Title: "전략", Width: 105}, {Title: "성공/전체", Width: 90}, {Title: "성공률", Width: 80}, {Title: "질문", Width: 80}, {Title: "HTTP (미상)", Width: 110}, {Title: "추정 비용($)", Width: 105}, {Title: "비용 미상", Width: 90}}},
		VSplitter{Children: []Widget{
			TableView{AssignTo: &v.trials, Model: &v.trialModel, OnCurrentIndexChanged: w.selectResearchTrial, Columns: []TableViewColumn{{Title: "전략", Width: 105}, {Title: "문제", Width: 170}, {Title: "구분", Width: 60}, {Title: "seed", Width: 60}, {Title: "상태", Width: 120}, {Title: "질문", Width: 75}}},
			TextEdit{AssignTo: &v.detail, ReadOnly: true, VScroll: true, HScroll: true},
		}},
		Composite{Layout: HBox{}, Children: []Widget{PushButton{Text: "선택 시도 판단 이력 열기", OnClicked: w.openResearchTrace}, HSpacer{}}},
	}}
}
func (w *window) loadResearch() {
	v := w.research
	if v == nil || v.path == nil {
		return
	}
	v.generation++
	gen := v.generation
	path := v.path.Text()
	v.study = researchresults.Study{}
	v.rows = nil
	v.summaryModel.set(nil)
	v.trialModel.set(nil)
	_ = v.detail.SetText("")
	_ = v.label.SetText("결과 불러오는 중")
	go func() {
		study, err := researchresults.Load(path)
		w.mw.Synchronize(func() {
			if gen != v.generation {
				return
			}
			if err != nil {
				_ = v.label.SetText("결과를 읽을 수 없습니다: " + err.Error())
				return
			}
			v.study = study
			strategies := []string{"전체"}
			problems := []string{"전체"}
			seen := map[string]bool{}
			for _, s := range study.Strategies {
				strategies = append(strategies, s.Strategy)
			}
			for _, t := range study.Trials {
				p := t.Outcome.Trial.Problem
				if !seen[p] {
					seen[p] = true
					problems = append(problems, p)
				}
			}
			sort.Strings(problems[1:])
			v.updating = true
			_ = v.strategy.SetModel(strategies)
			_ = v.strategy.SetCurrentIndex(0)
			_ = v.problem.SetModel(problems)
			_ = v.problem.SetCurrentIndex(0)
			v.updating = false
			_ = v.label.SetText(fmt.Sprintf("저장된 시도 %d / 계획 %d", len(study.Trials), study.Protocol.PlannedTrials))
			w.refreshResearch()
		})
	}()
}
func (w *window) refreshResearch() {
	v := w.research
	if v == nil || v.updating || v.split == nil || v.status == nil || v.strategy == nil || v.problem == nil {
		return
	}
	split := "all"
	if v.split.CurrentIndex() == 1 {
		split = "dev"
	} else if v.split.CurrentIndex() == 2 {
		split = "eval"
	}
	strategy := "all"
	si := v.strategy.CurrentIndex()
	if si > 0 && si <= len(v.study.Strategies) {
		strategy = v.study.Strategies[si-1].Strategy
	}
	problem := "all"
	if i := v.problem.CurrentIndex(); i > 0 {
		if model, ok := v.problem.Model().([]string); ok && i < len(model) {
			problem = model[i]
		}
	}
	status := "all"
	if v.status.CurrentIndex() == 1 {
		status = "success"
	} else if v.status.CurrentIndex() == 2 {
		status = "failure"
	}
	var summaries [][]interface{}
	for _, s := range v.study.Strategies {
		m := s.All
		if split == "dev" {
			m = s.Dev
		} else if split == "eval" {
			m = s.Eval
		}
		rate := "—"
		if m.Planned > 0 {
			rate = fmt.Sprintf("%.1f%%", 100*float64(m.Success)/float64(m.Planned))
		}
		summaries = append(summaries, []interface{}{s.Strategy, fmt.Sprintf("%d / %d", m.Success, m.Planned), rate, m.Questions, fmt.Sprintf("%d (%d)", m.HTTPRequests, m.HTTPUnknown), researchCost(m), m.UnknownCost})
	}
	v.summaryModel.set(summaries)
	var all []researchTrial
	for _, t := range v.study.Trials {
		all = append(all, researchTrial{Split: t.Split, Outcome: t.Outcome, RunID: t.RunID, TracePath: t.TracePath, TraceAvailable: t.TraceAvailable, ValidationMode: t.ValidationMode})
	}
	v.rows = filterResearchTrials(all, split, strategy, problem, status)
	var rows [][]interface{}
	for _, t := range v.rows {
		o := t.Outcome
		rows = append(rows, []interface{}{o.Trial.Strategy, o.Trial.Problem, t.Split, o.Trial.Seed, o.Result.Status, o.Result.Questions})
	}
	v.trialModel.set(rows)
	_ = v.detail.SetText("")
}
func (w *window) selectResearchTrial() {
	v := w.research
	i := v.trials.CurrentIndex()
	if i < 0 || i >= len(v.rows) {
		_ = v.detail.SetText("")
		return
	}
	_ = v.detail.SetText(researchTrialDetail(v.rows[i]))
}
func (w *window) openResearchTrace() {
	v := w.research
	i := v.trials.CurrentIndex()
	if i < 0 || i >= len(v.rows) {
		return
	}
	row := v.rows[i]
	if !row.TraceAvailable {
		_ = v.label.SetText("선택 시도의 판단 이력이 없습니다")
		return
	}
	s := w.manager.Snapshot()
	if s.Status == "running" || s.Status == "cancelling" {
		_ = v.label.SetText("실행이 끝난 뒤 판단 이력을 열 수 있습니다")
		return
	}
	w.cancelFrameRequest()
	w.cancelTraceLoad()
	ctx, cancel := context.WithCancel(context.Background())
	w.loadCancel = cancel
	gen := v.generation
	mgr := w.manager
	req := w.historyGuard.Begin(row.RunID, mgr)
	_ = v.label.SetText("선택 시도의 판단 이력 읽는 중")
	go func() {
		source := application.New(application.Config{OutputDir: filepath.Dir(row.TracePath)})
		index := &traceIndex{}
		var cursor application.TraceCursor
		var err error
		for {
			if ctx.Err() != nil {
				return
			}
			var events []application.Envelope
			var next application.TraceCursor
			var more bool
			events, next, more, err = source.ReadTraceBatch(row.RunID, cursor, 512)
			if err != nil {
				break
			}
			cursor = next
			index.appendEvents(traceEvents(researchTrialEvents(events, row.Outcome.TrialIndex)))
			if !more {
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		w.mw.Synchronize(func() {
			if gen != v.generation || !w.historyGuard.Accept(req, row.RunID, w.manager) || w.manager != mgr || v.trials.CurrentIndex() != i || i >= len(v.rows) || !sameResearchTrial(v.rows[i], row) || mgr.Snapshot().Status == "running" || mgr.Snapshot().Status == "cancelling" {
				return
			}
			if err != nil {
				_ = v.label.SetText("판단 이력을 읽을 수 없습니다")
				return
			}
			frames := index.framesView()
			if len(frames) == 0 {
				_ = v.label.SetText("선택 시도의 판단 이벤트가 없습니다")
				return
			}
			w.viewHistory = true
			w.currentRun = row.RunID
			w.traceIndex = index
			w.traceFinal = nil
			w.replay = Replay{}
			w.frames = nil
			for _, f := range frames {
				if w.replay.Add(f) {
					w.frames = append(w.frames, f)
				}
			}
			w.refreshFrames()
			w.replay.Pause()
			w.requestFrame(w.replay.View().Seq)
			_ = w.mainTabs.SetCurrentIndex(0)
			_ = v.label.SetText("선택 시도의 판단 이력 열림")
		})
	}()
}

func researchCost(m researchresults.Metrics) string {
	if m.KnownCostObservations == 0 {
		return "—"
	}
	return fmt.Sprintf("%.6f", m.KnownCost)
}
