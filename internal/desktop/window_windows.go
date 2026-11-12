//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"tessaro/internal/application"
	"tessaro/internal/compare"
	"tessaro/internal/modelprovider"
	"tessaro/internal/runner"
	"tessaro/internal/tokenspec"
)

type window struct {
	research                                                                               *researchView
	mw                                                                                     *walk.MainWindow
	manager                                                                                *application.Manager
	provider                                                                               *modelprovider.Provider
	watchStop                                                                              chan struct{}
	busy                                                                                   bool
	updatingParent, viewHistory, playing                                                   bool
	python, script, modelDir, benchmarkPath, projectRoot, compareModels, compareStrategies *walk.LineEdit
	manifest, output, spec, heldout, hypothesisPath                                        *walk.LineEdit
	model, problem, engine, strategy, parentFilter                                         *walk.ComboBox
	tokens, calls, candidates, timeout, locals, statements, nesting, repeats, depth        *walk.NumberEdit
	filter, historyFilter                                                                  *walk.LineEdit
	status, hypothesisStatus                                                               *walk.Label
	paths                                                                                  [3]*walk.TextEdit
	pathLabels                                                                             [3]*walk.Label
	candidateText, contextText, resultText, historyText, problemText                       *walk.TextEdit
	historyList, replayList                                                                *walk.ListBox
	models                                                                                 []compare.Model
	problems                                                                               []string
	records                                                                                []application.Record
	replay                                                                                 Replay
	historyGuard, historyListGuard                                                         HistoryGuard
	parentIDs                                                                              []string
	selectedParent                                                                         string
	frames, shownFrames                                                                    []Frame
	traceIndex                                                                             *traceIndex
	traceFinal                                                                             *application.Snapshot
	frameRequest                                                                           uint64
	frameCancel                                                                            context.CancelFunc
	loadCancel                                                                             context.CancelFunc
	framePending                                                                           bool
	currentRun                                                                             string
	budgetPanel                                                                            *walk.Composite
	sentTable, rejectedTable                                                               *walk.TableView
	sentModel, rejectedModel                                                               *candidateTable
	pathTabs, mainTabs, resultTabs                                                         *walk.TabWidget
	pathPages                                                                              [3]*walk.TabPage
	pathCode                                                                               [3]*walk.TextEdit
	pathIDs                                                                                []string
	selectedCandidate                                                                      string
	updatingRows                                                                           bool
	updatingList                                                                           bool
	decisionFrame                                                                          Frame
	choiceHeadline                                                                         *walk.Label
	previewMarker                                                                          [3]*walk.Label
	choiceText, countLabel                                                                 *walk.Label
	rawEvents                                                                              *walk.CheckBox
	detailsText                                                                            *walk.TextEdit
	sentPage, rejectedPage                                                                 *walk.TabPage
	actualCode                                                                             [3]*walk.TextEdit
	rawCode                                                                                *walk.CheckBox
	testsModel                                                                             *publicTestsModel
	testsTable                                                                             *walk.TableView
	codeTabs                                                                               [3]*walk.TabWidget
	historyPanel, contextPanel, candidatePanel                                             *walk.Composite
	followCode                                                                             *walk.CheckBox
	focusButton, candidateToggle                                                           *walk.PushButton
	fontSize                                                                               int
	previewSelectionPending                                                                bool
	userBrowsing                                                                           bool
	codeFocus, candidatesCollapsed                                                         bool
}

func Run() error {
	root := findRoot()
	defaults := desktopSettings{Project: root, Manifest: filepath.Join(root, "examples", "token", "compare-live.json"), Benchmark: filepath.Join(root, "examples", "benchmark.json"), Hypothesis: filepath.Join(root, "examples", "token", "hypothesis.json"), Python: filepath.Join(root, ".venv-rocm72", "Scripts", "python.exe"), Script: filepath.Join(root, "runtime", "local_worker.py"), ModelDir: filepath.Join(root, "runtime", "models", "decider-4b"), Output: filepath.Join(root, "output")}
	settings := loadSettings(defaults)
	w, err := newWindow(settings)
	if err != nil {
		return &StageError{Stage: StageWindowCreate}
	}
	w.loadManifest()
	w.loadHistory()
	w.watch()
	w.loadResearch()
	tickStop := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-tickStop:
				return
			case <-t.C:
				w.mw.Synchronize(func() {
					if w.playing {
						w.moveReplay(1)
					}
				})
			}
		}
	}()
	w.mw.Run()
	close(tickStop)
	w.cancelFrameRequest()
	w.cancelTraceLoad()
	close(w.watchStop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = w.manager.Close(ctx)
	if e := w.provider.Close(); err == nil {
		err = e
	}
	if err != nil {
		return &StageError{Stage: StageShutdown}
	}
	return nil
}

func newWindow(settings desktopSettings) (*window, error) {
	w := &window{sentModel: &candidateTable{}, rejectedModel: &candidateTable{}, testsModel: &publicTestsModel{}, fontSize: 16}
	w.provider = modelprovider.New(modelprovider.Config{Python: settings.Python, Script: settings.Script, ModelDir: settings.ModelDir})
	var err error
	w.manager = application.New(application.Config{OutputDir: settings.Output, Resolve: w.provider.Resolve})
	err = MainWindow{AssignTo: &w.mw, Title: "Tessaro", Font: Font{Family: "맑은 고딕", PointSize: 10}, MinSize: Size{Width: 900, Height: 650}, Size: Size{Width: 1320, Height: 850}, Layout: VBox{}, Children: []Widget{
		TabWidget{AssignTo: &w.mainTabs, Pages: []TabPage{
			w.searchPage(),
			w.researchPage(settings.Project),
			{Title: "기록", Layout: VBox{}, Children: []Widget{Composite{Layout: HBox{}, Children: []Widget{PushButton{Text: "새로고침", OnClicked: w.loadHistory}, Label{Text: "문제 ID 필터"}, LineEdit{AssignTo: &w.historyFilter, OnTextChanged: func() { w.refreshFrames() }}, PushButton{Text: "선택 기록 열기", OnClicked: w.openHistory}, PushButton{Text: "JSON 내보내기", OnClicked: w.exportHistory}, PushButton{Text: "Trace 내보내기", OnClicked: w.exportTrace}, PushButton{Text: "성공 코드 내보내기", OnClicked: w.exportCode}, PushButton{Text: "클립보드 복사", OnClicked: func() { _ = walk.Clipboard().SetText(w.historyText.Text()) }}}}, HSplitter{Children: []Widget{ListBox{AssignTo: &w.historyList}, TextEdit{AssignTo: &w.historyText, ReadOnly: true, VScroll: true, HScroll: true}}}}},
			{Title: "비교 / 벤치마크", Layout: VBox{}, Children: []Widget{Label{Text: "비교 모델 ID (쉼표로 구분; 빈칸이면 선택 모델)"}, LineEdit{AssignTo: &w.compareModels}, Label{Text: "전략 (쉼표로 구분)"}, LineEdit{AssignTo: &w.compareStrategies, Text: "greedy,beam"}, Label{Text: "반복 횟수"}, NumberEdit{AssignTo: &w.repeats, MinValue: 1, MaxValue: 10000, Value: 1.0}, Label{Text: "설정의 manifest와 선택한 엔진을 사용합니다. 진행 및 결과는 탐색 탭과 기록에서 확인할 수 있습니다."}, Composite{Layout: HBox{}, Children: []Widget{PushButton{Text: "비교 시작", OnClicked: func() { w.start("compare") }}, PushButton{Text: "벤치마크 시작", OnClicked: func() { w.start("benchmark") }}, PushButton{Text: "중지", OnClicked: w.cancel}, HSpacer{}}}, Label{Text: "가설 검증: 40문제 · Jev 1.14.0 · 최대10,000회 API 요청 · 실제요금발생"}, Label{Text: "선택 모델과 관계없이 전용 manifest의 Jev를 사용합니다."}, Composite{Layout: HBox{}, Children: []Widget{PushButton{Text: "가설 검증 시작", OnClicked: func() { w.start("hypothesis") }}, PushButton{Text: "중지", OnClicked: w.cancel}, PushButton{Text: "결과 열기", OnClicked: w.showHypothesisResult}, HSpacer{}}}, Label{AssignTo: &w.hypothesisStatus, Text: "준비"}}},
			{Title: "설정", Layout: VBox{}, Children: []Widget{
				Label{Text: "프로젝트 폴더"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.projectRoot, Text: settings.Project}, PushButton{Text: "찾기", OnClicked: func() { w.chooseDirectory(w.projectRoot) }}, PushButton{Text: "기본 경로 채우기", OnClicked: w.projectDefaults}}}, Label{Text: "문제 및 모델 manifest"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.manifest, Text: settings.Manifest}, PushButton{Text: "찾기", OnClicked: func() { w.chooseFile(w.manifest) }}, PushButton{Text: "목록 읽기", OnClicked: w.loadManifest}}},
				Label{Text: "개별 문제 파일 (선택 사항)"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.spec}, PushButton{Text: "찾기", OnClicked: func() { w.chooseFile(w.spec) }}}},
				Label{Text: "개별 heldout 파일 (선택 사항)"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.heldout}, PushButton{Text: "찾기", OnClicked: func() { w.chooseFile(w.heldout) }}}},
				Label{Text: "벤치마크 입력 파일"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.benchmarkPath, Text: settings.Benchmark}, PushButton{Text: "찾기", OnClicked: func() { w.chooseFile(w.benchmarkPath) }}}}, Label{Text: "가설 검증 manifest"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.hypothesisPath, Text: settings.Hypothesis}, PushButton{Text: "찾기", OnClicked: func() { w.chooseFile(w.hypothesisPath) }}}}, Label{Text: "Python 실행 파일"}, LineEdit{AssignTo: &w.python, Text: settings.Python}, Label{Text: "모델 worker 스크립트"}, LineEdit{AssignTo: &w.script, Text: settings.Script}, Label{Text: "모델 폴더"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.modelDir, Text: settings.ModelDir}, PushButton{Text: "찾기", OnClicked: func() { w.chooseDirectory(w.modelDir) }}}}, Label{Text: "결과 폴더 (변경 후 적용)"}, Composite{Layout: HBox{}, Children: []Widget{LineEdit{AssignTo: &w.output, Text: settings.Output}, PushButton{Text: "찾기", OnClicked: w.chooseFolder}, PushButton{Text: "적용", OnClicked: w.applyOutput}}}, VSpacer{},
			}},
		}},
	}}.Create()
	if err != nil {
		return nil, err
	}
	// 2026-11-08: 후보 표에 포커스가 있어도 코드의 추가 토큰 선택 표시가 유지되어야 한다.
	for _, code := range w.pathCode {
		style := win.GetWindowLong(code.Handle(), win.GWL_STYLE)
		win.SetWindowLong(code.Handle(), win.GWL_STYLE, style|win.ES_NOHIDESEL)
	}
	w.budgetPanel.SetVisible(false)
	w.mw.Starting().Attach(func() { win.ShowWindow(w.mw.Handle(), win.SW_MAXIMIZE) })
	return w, nil
}

type desktopSettings struct{ Project, Manifest, Benchmark, Hypothesis, Python, Script, ModelDir, Output string }

func settingsPath() string {
	d, e := os.UserConfigDir()
	if e != nil {
		return ""
	}
	return filepath.Join(d, "Tessaro", "settings.json")
}
func loadSettings(fallback desktopSettings) desktopSettings {
	path := settingsPath()
	if path == "" {
		return fallback
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return fallback
	}
	var s desktopSettings
	if json.Unmarshal(data, &s) != nil || s.Project == "" || s.Manifest == "" || s.Python == "" || s.Script == "" || s.ModelDir == "" || s.Output == "" {
		return fallback
	}
	if s.Benchmark == "" {
		s.Benchmark = fallback.Benchmark
	}
	if s.Hypothesis == "" {
		s.Hypothesis = fallback.Hypothesis
	}
	return s
}
func saveSettings(s desktopSettings) error {
	path := settingsPath()
	if path == "" {
		return fmt.Errorf("config unavailable")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, b, 0600)
}
func findRoot() string {
	paths := []string{}
	if e, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Dir(e))
	}
	if d, e := os.Getwd(); e == nil {
		paths = append(paths, d)
	}
	for _, p := range paths {
		for {
			if _, e := os.Stat(filepath.Join(p, "examples", "token", "compare-live.json")); e == nil {
				return p
			}
			n := filepath.Dir(p)
			if n == p {
				break
			}
			p = n
		}
	}
	return "."
}
func (w *window) alert(s string) { walk.MsgBox(w.mw, "Tessaro", s, walk.MsgBoxIconError) }
func (w *window) chooseFile(edit *walk.LineEdit) {
	d := new(walk.FileDialog)
	d.Filter = "JSON 파일 (*.json)|*.json|모든 파일 (*.*)|*.*"
	if ok, _ := d.ShowOpen(w.mw); ok {
		_ = edit.SetText(d.FilePath)
	}
}
func (w *window) projectDefaults() {
	root := w.projectRoot.Text()
	_ = w.manifest.SetText(filepath.Join(root, "examples", "token", "compare-live.json"))
	_ = w.benchmarkPath.SetText(filepath.Join(root, "examples", "benchmark.json"))
	_ = w.hypothesisPath.SetText(filepath.Join(root, "examples", "token", "hypothesis.json"))
	_ = w.python.SetText(filepath.Join(root, ".venv-rocm72", "Scripts", "python.exe"))
	_ = w.script.SetText(filepath.Join(root, "runtime", "local_worker.py"))
	_ = w.modelDir.SetText(filepath.Join(root, "runtime", "models", "decider-4b"))
	_ = w.output.SetText(filepath.Join(root, "output"))
	w.loadManifest()
}
func (w *window) chooseDirectory(edit *walk.LineEdit) {
	d := new(walk.FileDialog)
	if ok, _ := d.ShowBrowseFolder(w.mw); ok {
		_ = edit.SetText(d.FilePath)
	}
}
func (w *window) chooseFolder() { w.chooseDirectory(w.output) }
func (w *window) watch() {
	manager := w.manager
	stop := make(chan struct{})
	w.watchStop = stop
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		var pending atomic.Bool
		dirty := false
		var runID string
		var cursor application.TraceCursor
		var index *traceIndex
		for {
			select {
			case <-stop:
				return
			case <-manager.Changed():
				dirty = true
			case <-ticker.C:
				if !dirty || !pending.CompareAndSwap(false, true) {
					continue
				}
				s := manager.Snapshot()
				var frames []Frame
				var readErr error
				if s.JobKind == "hypothesis" && index != nil {
					runID, cursor, index = "", application.TraceCursor{}, nil
				}
				if s.RunID != "" && s.JobKind != "hypothesis" {
					if runID != s.RunID {
						runID, cursor, index = s.RunID, application.TraceCursor{}, &traceIndex{}
					}
					for {
						select {
						case <-stop:
							pending.Store(false)
							return
						default:
						}
						events, next, more, e := manager.ReadTraceBatch(runID, cursor, 512)
						if e != nil {
							readErr = e
							break
						}
						cursor = next
						frames = append(frames, index.appendEvents(traceEvents(events))...)
						if !more {
							break
						}
					}
				}
				dirty = readErr != nil
				var current Frame
				if index != nil {
					current = index.currentFrame()
				}
				w.mw.Synchronize(func() {
					defer pending.Store(false)
					if w.manager == manager {
						w.update(s, frames, index, current)
						if readErr != nil {
							_ = w.status.SetText("실행 기록 읽기 오류 · 다시 시도 중")
						}
					}
				})
			}
		}
	}()
}
func (w *window) loadManifest() {
	path := w.manifest.Text()
	go func() {
		f, e := os.Open(path)
		if e != nil {
			w.mw.Synchronize(func() { w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.") })
			return
		}
		m, e := compare.Decode(f)
		_ = f.Close()
		w.mw.Synchronize(func() {
			if e != nil {
				w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.")
				return
			}
			w.models = nil
			var names []string
			def := 0
			for _, v := range m.Models {
				if v.Ranker == "fake" {
					continue
				}
				if v.ID == "decider" {
					def = len(names)
				}
				w.models = append(w.models, v)
				names = append(names, v.ID)
			}
			w.problems = []string{"all"}
			for _, p := range m.Problems {
				w.problems = append(w.problems, p.ID)
			}
			_ = w.model.SetModel(names)
			if len(names) > 0 {
				_ = w.model.SetCurrentIndex(def)
			}
			_ = w.problem.SetModel(w.problems)
			_ = w.problem.SetCurrentIndex(0)
			if m.Engine == "ast" {
				_ = w.engine.SetCurrentIndex(1)
			} else {
				_ = w.engine.SetCurrentIndex(0)
			}
		})
	}()
}
func (w *window) applyOutput() {
	if w.busy {
		return
	}
	s := w.manager.Snapshot()
	if s.Status == "running" || s.Status == "cancelling" {
		w.alert("실행을 중지한 후 설정을 적용하세요.")
		return
	}
	w.busy = true
	oldManager, oldProvider := w.manager, w.provider
	cfg := modelprovider.Config{Python: w.python.Text(), Script: w.script.Text(), ModelDir: w.modelDir.Text()}
	output := w.output.Text()
	saved := desktopSettings{Project: w.projectRoot.Text(), Manifest: w.manifest.Text(), Benchmark: w.benchmarkPath.Text(), Hypothesis: w.hypothesisPath.Text(), Python: cfg.Python, Script: cfg.Script, ModelDir: cfg.ModelDir, Output: output}
	go func() {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		e := saveSettings(saved)
		if e == nil {
			e = oldManager.Close(ctx)
		}
		if e == nil {
			e = oldProvider.Close()
		}
		w.mw.Synchronize(func() {
			w.busy = false
			if e != nil {
				w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.")
				return
			}
			w.cancelFrameRequest()
			w.cancelTraceLoad()
			close(w.watchStop)
			w.traceIndex, w.traceFinal = nil, nil
			w.frames, w.shownFrames = nil, nil
			w.replay, w.currentRun = Replay{}, ""
			w.provider = modelprovider.New(cfg)
			w.manager = application.New(application.Config{OutputDir: output, Resolve: w.provider.Resolve})
			w.watch()
			w.loadHistory()
			_ = w.status.SetText("설정 적용됨")
		})
	}()
}
func strategiesForEngine(engine int) []string {
	if engine == 1 {
		return []string{"beam", "greedy", "strict-greedy"}
	}
	return []string{"beam", "greedy", "strict-greedy", "lds", "levin", "phs", "mcts"}
}

func (w *window) updateStrategies() {
	if w.strategy == nil || w.engine == nil {
		return
	}
	index := w.strategy.CurrentIndex()
	choices := strategiesForEngine(w.engine.CurrentIndex())
	if index < 0 || index >= len(choices) {
		index = 0
	}
	_ = w.strategy.SetModel(choices)
	_ = w.strategy.SetCurrentIndex(index)
}
func (w *window) runOptions() runner.Request {
	return runner.Request{ReturnPolicy: tokenspec.ReturnPolicyValuesOnly, Seed: 42, DecisionBatchSize: 1, MaxTokens: int(w.tokens.Value()), MaxCalls: int(w.calls.Value()), MaxCandidates: int(w.candidates.Value()), MaxLocals: int(w.locals.Value()), LocalLimitSet: true, MaxStatements: int(w.statements.Value()), MaxNesting: int(w.nesting.Value()), MaxDepth: int(w.depth.Value()), Timeout: time.Duration(w.timeout.Value()) * time.Second, TraceCode: true, TraceDecisionContext: true}
}
func (w *window) start(kind string) {
	if w.busy {
		return
	}
	if kind == "hypothesis" {
		w.startHypothesis()
		return
	}
	i := w.model.CurrentIndex()
	if i < 0 || i >= len(w.models) {
		w.alert("사용할 모델을 선택하세요.")
		return
	}
	p := "all"
	if j := w.problem.CurrentIndex(); j >= 0 && j < len(w.problems) {
		p = w.problems[j]
	}
	req := application.Request{Kind: kind, Engine: []string{"token", "ast"}[w.engine.CurrentIndex()], Strategy: strategiesForEngine(w.engine.CurrentIndex())[w.strategy.CurrentIndex()], ManifestPath: w.manifest.Text(), SpecPath: w.spec.Text(), HeldoutPath: w.heldout.Text(), ProblemID: p, Model: w.models[i], Options: w.runOptions()}
	modelIDs := w.compareModels.Text()
	strategies := w.compareStrategies.Text()
	repeats := int(w.repeats.Value())
	if kind == "benchmark" {
		req.ManifestPath = w.benchmarkPath.Text()
	}
	manager := w.manager
	w.busy = true
	go func() {
		var e error
		if kind == "compare" {
			req.Manifest, e = prepareComparison(req.ManifestPath, req.Engine, req.Model, modelIDs, strategies, repeats)
		}
		if e == nil {
			_, e = manager.Start(req)
		}
		w.mw.Synchronize(func() {
			w.busy = false
			if e != nil {
				w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.")
			}
		})
	}()
}
func (w *window) startHypothesis() {
	req := application.Request{Kind: "hypothesis", ManifestPath: w.hypothesisPath.Text()}
	manager := w.manager
	w.busy = true
	go func() {
		_, err := manager.Start(req)
		w.mw.Synchronize(func() {
			w.busy = false
			if err != nil {
				w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.")
			}
		})
	}()
}
func (w *window) showHypothesisResult() {
	s := w.manager.Snapshot()
	if s.JobKind != "hypothesis" || len(s.Payload) == 0 {
		walk.MsgBox(w.mw, "가설 검증 결과", "표시할 가설 검증 결과가 없습니다. 이전 결과는 기록에서 확인하세요.", walk.MsgBoxIconInformation)
		return
	}
	_ = w.resultText.SetText(formatResult(s))
	_ = w.mainTabs.SetCurrentIndex(0)
	_ = w.resultTabs.SetCurrentIndex(3)
}
func (w *window) cancel() {
	manager := w.manager
	go func() {
		if e := manager.Cancel(); e != nil {
			w.mw.Synchronize(func() { w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.") })
		}
	}()
}
func pretty(v any) string {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return "표시할 수 없음"
	}
	return string(b)
}
func statusText(s application.Snapshot) string {
	labels := map[string]string{"running": "모델 준비 또는 작업 중", "cancelling": "중지 중", "completed": "완료", "cancelled": "중지됨", "failed": "실패", "storage_error": "기록 저장 실패", "interrupted": "중단됨"}
	label := labels[s.Status]
	if s.Status == "running" && s.Phase == "model_preparing" {
		label = "모델 준비 중"
	}
	if s.Status == "running" && s.Phase == "ready" {
		label = "실행 중"
	}
	if label == "" {
		label = "상태 확인 중"
	}
	errors := map[string]string{"model_unavailable": "모델을 사용할 수 없습니다", "unsupported_local_model": "지원하지 않는 로컬 모델", "local_runtime_not_configured": "로컬 실행 경로가 설정되지 않았습니다", "worker_unavailable": "모델 프로세스를 사용할 수 없습니다", "worker_start_failed": "모델 프로세스 시작 실패", "worker_start_timeout": "모델 준비 시간 초과", "worker_initialization_failed": "모델 초기화 실패", "worker_request_timeout": "모델 요청 시간 초과", "worker_inference_failed": "모델 추론 실패", "storage_error": "기록 저장 실패", "invalid_input": "입력이 올바르지 않습니다"}
	if message := errors[s.Error]; message != "" {
		label += " · " + message
	}
	if s.JobKind == "hypothesis" {
		phase := map[string]string{"reference": "기준 확인", "reachability": "도달 가능성", "prefix": "접두사 평가", "generation": "생성 평가"}[s.Phase]
		if phase == "" {
			phase = s.Phase
		}
		return fmt.Sprintf("%s · %s  %d/%d · API 요청 %d/%d", label, phase, s.Completed, s.Total, s.RequestsUsed, s.RequestLimit)
	}
	return fmt.Sprintf("%s  %d/%d", label, s.Completed, s.Total)
}
func (w *window) update(s application.Snapshot, incoming []Frame, index *traceIndex, current Frame) {
	if s.JobKind == "hypothesis" {
		_ = w.hypothesisStatus.SetText(statusText(s))
		_ = w.status.SetText(statusText(s))
		if w.viewHistory || s.RunID == "" {
			return
		}
		if w.currentRun != s.RunID {
			w.currentRun = s.RunID
			w.replay = Replay{}
			w.frames = nil
			w.traceIndex = nil
			w.traceFinal = nil
			w.cancelFrameRequest()
			w.refreshFrames()
		}
		if len(s.Payload) > 0 {
			_ = w.resultText.SetText(formatResult(s))
		}
		return
	}
	if w.viewHistory {
		_ = w.status.SetText(statusText(s))
		return
	}
	if s.RunID == "" {
		return
	}
	if w.currentRun != s.RunID {
		w.userBrowsing = false
		w.currentRun = s.RunID
		w.replay = Replay{}
		w.frames = nil
		w.traceIndex = nil
		w.traceFinal = nil
		w.cancelFrameRequest()
	}
	restorePaused, restoreSeq := false, uint64(0)
	if index != nil && w.traceIndex != index {
		restorePaused, restoreSeq = w.replay.paused, w.replay.View().Seq
		w.traceIndex = index
		w.traceFinal = nil
		w.frames = nil
		w.replay = Replay{}
		w.cancelFrameRequest()
		incoming = index.framesView()
	}
	for _, f := range incoming {
		if len(w.frames) > 0 && w.frames[len(w.frames)-1].Seq == f.Seq && w.frames[len(w.frames)-1].RunID == f.RunID {
			w.frames[len(w.frames)-1] = f
			w.replay.Merge(f)
		} else if w.replay.Add(f) {
			w.frames = append(w.frames, f)
		}
	}
	if restorePaused && w.replay.Select(restoreSeq) {
		w.requestFrame(restoreSeq)
	}
	w.refreshFrames()
	_ = w.status.SetText(statusText(s))
	if w.replay.paused {
		// 정지한 화면은 선택 위치를 유지한다. 새 이벤트 수신 시 재구성하지 않는다.
	} else {
		live := current
		if s.JobKind != "compare" {
			live.Text = pretty(s)
		}
		w.showFrame(live)
	}
}
func traceEvents(events []application.Envelope) []TraceEvent {
	items := make([]TraceEvent, 0, len(events))
	for _, e := range events {
		items = append(items, TraceEvent{Seq: e.Seq, RunID: e.RunID, Kind: e.Kind, Data: e.Data})
	}
	return items
}
func (w *window) live() {
	_ = w.historyGuard.Begin("", w.manager)
	w.cancelTraceLoad()
	w.viewHistory = false
	w.playing = false
	w.currentRun = ""
	w.cancelFrameRequest()
	manager := w.manager
	request := w.frameRequest
	ctx, cancel := context.WithCancel(context.Background())
	w.loadCancel = cancel
	go func() {
		s := manager.Snapshot()
		index, e := loadTraceIndex(ctx, manager, s.RunID)
		var frames []Frame
		var current Frame
		if e == nil {
			frames = index.framesView()
			current = index.currentFrame()
		}
		if ctx.Err() != nil {
			return
		}
		w.mw.Synchronize(func() {
			if w.manager == manager && w.frameRequest == request && !w.viewHistory {
				w.update(s, frames, index, current)
				if e != nil {
					_ = w.status.SetText("실행 기록을 읽을 수 없습니다.")
				}
			}
		})
	}()
}

func loadTraceIndex(ctx context.Context, manager *application.Manager, id string) (*traceIndex, error) {
	index := &traceIndex{}
	var cursor application.TraceCursor
	for {
		select {
		case <-ctx.Done():
			return index, context.Canceled
		default:
		}
		events, next, more, err := manager.ReadTraceBatch(id, cursor, 512)
		if err != nil {
			return index, err
		}
		cursor = next
		index.appendEvents(traceEvents(events))
		if !more {
			return index, nil
		}
	}
}
func (w *window) moveReplay(delta int) {
	if w.playing && w.framePending {
		return
	}
	if len(w.shownFrames) == 0 {
		return
	}
	seq := w.replay.View().Seq
	i := 0
	for n, f := range w.shownFrames {
		if f.Seq == seq {
			i = n
			break
		}
	}
	i += delta
	if i < 0 {
		i = 0
	}
	if i >= len(w.shownFrames) {
		i = len(w.shownFrames) - 1
		w.playing = false
	}
	if w.replay.Select(w.shownFrames[i].Seq) {
		w.requestFrame(w.replay.View().Seq)
		_ = w.replayList.SetCurrentIndex(i)
	}
}
func (w *window) copyPath(i int) {
	var s application.Snapshot
	if json.Unmarshal([]byte(w.decisionFrame.Text), &s) == nil && i < len(s.Frontier) {
		_ = walk.Clipboard().SetText(s.Frontier[i].Code)
	}
}
func (w *window) refreshFrames() {
	if w.replayList == nil || w.historyFilter == nil {
		return
	}
	q := strings.ToLower(w.historyFilter.Text())
	w.shownFrames = nil
	var labels []string
	frames := w.frames
	if w.rawEvents != nil && !w.rawEvents.Checked() {
		frames = decisionTimeline(frames)
	}
	for _, f := range frames {
		id := f.ProblemID
		if q == "" || strings.Contains(strings.ToLower(id), q) {
			w.shownFrames = append(w.shownFrames, f)
			label := fmt.Sprintf("판단 %d · %s", f.Step, id)
			if f.DecisionStatus == "forced" {
				label += " · 자동: " + f.Choice
			} else if f.Choice != "" {
				label += " · 선택: " + f.Choice
			} else if f.DecisionStatus == "calling" {
				label += " · 응답 대기"
			} else if f.DecisionStatus == "failed" {
				label += " · 실패"
			}
			if w.rawEvents != nil && w.rawEvents.Checked() {
				label = fmt.Sprintf("%d · %s", f.Seq, label)
			}
			labels = append(labels, label)
		}
	}
	w.updatingList = true
	_ = w.replayList.SetModel(labels)
	for i, frame := range w.shownFrames {
		if frame.Seq == w.replay.View().Seq {
			_ = w.replayList.SetCurrentIndex(i)
			break
		}
	}
	w.updatingList = false
}
func (w *window) selectFrame() {
	if w.updatingList {
		return
	}
	i := w.replayList.CurrentIndex()
	if i >= 0 && i < len(w.shownFrames) && w.replay.Select(w.shownFrames[i].Seq) {
		w.requestFrame(w.replay.View().Seq)
	}
}

func (w *window) requestFrame(seq uint64) {
	index := w.traceIndex
	if index == nil {
		w.showFrame(w.replay.View())
		return
	}
	w.cancelFrameRequest()
	request, run, manager := w.frameRequest, w.currentRun, w.manager
	ctx, cancel := context.WithCancel(context.Background())
	w.frameCancel = cancel
	w.framePending = true
	go func() {
		frame := index.frameAtContext(ctx, seq)
		if ctx.Err() != nil {
			return
		}
		w.mw.Synchronize(func() {
			if request != w.frameRequest || w.traceIndex != index || w.currentRun != run || w.manager != manager || !w.replay.paused || w.replay.View().Seq != seq {
				return
			}
			w.framePending = false
			frame = mergeReplayFinal(frame, w.traceFinal, seq)
			w.showFrame(frame)
		})
	}()
}

func (w *window) cancelFrameRequest() {
	w.frameRequest++
	w.framePending = false
	if w.frameCancel != nil {
		w.frameCancel()
		w.frameCancel = nil
	}
}

func (w *window) cancelTraceLoad() {
	if w.loadCancel != nil {
		w.loadCancel()
		w.loadCancel = nil
	}
}
func (w *window) showFrame(f Frame) {
	if f.Text == "" {
		return
	}
	var s application.Snapshot
	if decodeDisplayJSON([]byte(f.Text), &s) != nil {
		return
	}
	w.showDecision(f, s)
}
func (w *window) loadHistory() {
	manager := w.manager
	request := w.historyListGuard.Begin("list", manager)
	go func() {
		records, e := manager.History()
		w.mw.Synchronize(func() {
			if !w.historyListGuard.Accept(request, "list", w.manager) {
				return
			}
			if e != nil {
				w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.")
				return
			}
			w.records = records
			var labels []string
			for _, r := range records {
				labels = append(labels, fmt.Sprintf("%s  %s  %s", r.ID, r.Kind, r.Status))
			}
			_ = w.historyList.SetModel(labels)
		})
	}()
}
func (w *window) openHistory() {
	id := w.selectedRecordID()
	if id == "" {
		return
	}
	manager := w.manager
	w.cancelFrameRequest()
	w.cancelTraceLoad()
	ctx, cancel := context.WithCancel(context.Background())
	w.loadCancel = cancel
	request := w.historyGuard.Begin(id, manager)
	go func() {
		r, e := manager.ReadHistory(id)
		var frames []Frame
		var index *traceIndex
		var value any
		var final application.Snapshot
		if e == nil && (r.Kind == "desktop_run" || r.Kind == "web_run") {
			_ = json.Unmarshal(r.Data, &final)
		}
		if e == nil && r.Trace && final.JobKind != "hypothesis" {
			index, e = loadTraceIndex(ctx, manager, id)
			if e == nil {
				frames = index.framesView()
			}
		}
		if e == nil {
			_ = json.Unmarshal(r.Data, &value)
			if r.Kind == "desktop_run" || r.Kind == "web_run" {
				_ = json.Unmarshal(r.Data, &final)
				if final.RunID == id {
					frames = MergeSnapshot(frames, final)
				}
			}
		}

		if ctx.Err() != nil {
			return
		}
		w.mw.Synchronize(func() {
			if !w.historyGuard.Accept(request, w.selectedRecordID(), w.manager) {
				return
			}
			if e != nil {
				w.alert("기록을 읽을 수 없습니다.")
				return
			}
			if final.JobKind == "hypothesis" {
				_ = w.historyText.SetText(formatResult(final) + "\r\n\r\n상세 기록은 Trace 내보내기로 확인하세요.")
			} else {
				_ = w.historyText.SetText(pretty(value))
			}
			w.viewHistory = true
			w.currentRun = id
			w.replay = Replay{}
			w.frames = nil
			w.traceIndex = index
			w.traceFinal = &final
			w.cancelFrameRequest()
			for _, f := range frames {
				if w.replay.Add(f) {
					w.frames = append(w.frames, f)
				}
			}
			w.refreshFrames()
			w.replay.Pause()
			w.requestFrame(w.replay.View().Seq)
			if final.RunID == id && len(final.Payload) > 0 && (len(frames) == 0 || frames[len(frames)-1].Seq != final.Seq) {
				_ = w.resultText.SetText(formatResult(final))
			}
		})
	}()
}
func (w *window) exportHistory() {
	i := w.historyList.CurrentIndex()
	if i < 0 || i >= len(w.records) {
		return
	}
	id := w.records[i].ID
	d := new(walk.FileDialog)
	d.Filter = "JSON 파일 (*.json)|*.json"
	d.FilePath = id + ".json"
	ok, e := d.ShowSave(w.mw)
	if e != nil || !ok {
		return
	}
	path := d.FilePath
	manager := w.manager
	go func() {
		r, e := manager.ReadHistory(id)
		if e == nil {
			e = os.WriteFile(path, r.Data, 0600)
		}
		if e != nil {
			w.mw.Synchronize(func() { w.alert("요청을 완료할 수 없습니다. 입력과 실행 상태를 확인하세요.") })
		}
	}()
}

func (w *window) selectedRecordID() string {
	i := w.historyList.CurrentIndex()
	if i < 0 || i >= len(w.records) {
		return ""
	}
	return w.records[i].ID
}
func (w *window) savePath(filter, name string) string {
	d := new(walk.FileDialog)
	d.Filter = filter
	d.FilePath = name
	ok, e := d.ShowSave(w.mw)
	if e != nil || !ok {
		return ""
	}
	return d.FilePath
}
func (w *window) exportTrace() {
	id := w.selectedRecordID()
	if id == "" {
		return
	}
	path := w.savePath("JSON Lines (*.jsonl)|*.jsonl", id+".jsonl")
	if path == "" {
		return
	}
	manager := w.manager
	go func() {
		e := exportTraceFile(context.Background(), manager, id, path)
		if e != nil {
			w.mw.Synchronize(func() { w.alert("Trace를 내보낼 수 없습니다.") })
		}
	}()
}
func (w *window) exportCode() {
	id := w.selectedRecordID()
	if id == "" {
		return
	}
	manager := w.manager
	problemID := strings.TrimSpace(w.historyFilter.Text())
	go func() {
		r, e := manager.ReadHistory(id)
		if e != nil {
			w.mw.Synchronize(func() { w.alert("기록을 읽을 수 없습니다.") })
			return
		}
		var s application.Snapshot
		_ = json.Unmarshal(r.Data, &s)
		var codes []string
		for _, p := range s.Results {
			if (problemID == "" || p.ProblemID == problemID) && p.Result.Status == "success" && p.Result.Source != "" {
				codes = append(codes, p.Result.Source)
			}
		}
		if len(s.Results) == 0 {
			var legacy runner.Result
			if json.Unmarshal(r.Data, &legacy) == nil && legacy.Status == "success" && legacy.Source != "" {
				codes = append(codes, legacy.Source)
			}
		}
		if len(codes) != 1 {
			w.mw.Synchronize(func() {
				w.alert("성공한 문제 ID를 정확히 입력하거나 단일 성공 기록을 선택하세요.")
			})
			return
		}
		code := codes[0]
		w.mw.Synchronize(func() {
			path := w.savePath("Go 소스 (*.go)|*.go", id+".go")
			if path != "" {
				go func() {
					if os.WriteFile(path, []byte(code), 0600) != nil {
						w.mw.Synchronize(func() { w.alert("코드를 저장할 수 없습니다.") })
					}
				}()
			}
		})
	}()
}

func prepareComparison(path, engine string, selected compare.Model, ids, strategies string, repeats int) (*compare.Manifest, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	m, e := compare.Decode(f)
	_ = f.Close()
	if e != nil {
		return nil, e
	}
	wanted := map[string]bool{}
	if strings.TrimSpace(ids) == "" {
		wanted[selected.ID] = true
	} else {
		for _, id := range strings.Split(ids, ",") {
			wanted[strings.TrimSpace(id)] = true
		}
	}
	var models []compare.Model
	for _, model := range m.Models {
		if wanted[model.ID] && model.Ranker != "fake" {
			models = append(models, model)
			delete(wanted, model.ID)
		}
	}
	if len(wanted) > 0 || len(models) == 0 {
		return nil, fmt.Errorf("invalid model selection")
	}
	m.Models = models
	m.Strategies = nil
	for _, strategy := range strings.Split(strategies, ",") {
		m.Strategies = append(m.Strategies, strings.TrimSpace(strategy))
	}
	m.Repeats = repeats
	m.Engine = engine
	data, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	if _, e = compare.Decode(strings.NewReader(string(data))); e != nil {
		return nil, e
	}
	return &m, nil
}
