package hypothesis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"jevcancode/internal/compare"
	"jevcancode/internal/rank"
	"jevcancode/internal/tokenspec"
	"jevcancode/internal/tokensynth"
	"jevcancode/internal/tokenverify"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

type Progress struct {
	Phase                                        string
	ProblemID                                    string
	Completed, Total, RequestsUsed, RequestLimit int
	Event                                        *DetailEvent
}
type DetailEvent struct {
	Phase        string           `json:"phase"`
	ProblemID    string           `json:"problem_id"`
	Kind         string           `json:"kind"`
	Step         int              `json:"step"`
	State        string           `json:"state,omitempty"`
	Candidates   []rank.Candidate `json:"candidates,omitempty"`
	Scores       []rank.Score     `json:"scores,omitempty"`
	ChosenID     string           `json:"chosen_id,omitempty"`
	ParentID     string           `json:"parent_id,omitempty"`
	Lexeme       string           `json:"lexeme,omitempty"`
	Reason       string           `json:"reason,omitempty"`
	Rank         int              `json:"rank,omitempty"`
	ID           string           `json:"id,omitempty"`
	Seed         *int64           `json:"seed,omitempty"`
	BranchStatus string           `json:"branch_status,omitempty"`
	Usage        *rank.Usage      `json:"usage,omitempty"`
}
type ProblemResult struct {
	ID                      string `json:"id"`
	Status                  string `json:"status"`
	Reason                  string `json:"reason,omitempty"`
	Position                int    `json:"position,omitempty"`
	Calls                   int    `json:"calls"`
	Tokens                  int    `json:"tokens"`
	Forced                  int    `json:"forced"`
	ExceedsGenerationBudget bool   `json:"exceeds_generation_budget"`
}
type Report struct {
	Ranking           *BResult           `json:"ranking,omitempty"`
	Generation        []GenerationResult `json:"generation,omitempty"`
	ActualModel       string             `json:"actual_model,omitempty"`
	Usage             *rank.Usage        `json:"usage,omitempty"`
	PlannedGeneration int                `json:"planned_generation"`
	Problems          []ProblemResult    `json:"problems,omitempty"`
	SchemaVersion     int                `json:"schema_version"`
	EvaluationKind    string             `json:"evaluation_kind"`
	Status            string             `json:"status"`
	RequestsUsed      int                `json:"requests_used"`
	RequestLimit      int                `json:"request_limit"`
	Summary           string             `json:"summary"`
	Fingerprints      map[string]string  `json:"fingerprints,omitempty"`
	BuildRevision     string             `json:"build_revision,omitempty"`
	BuildModified     string             `json:"build_modified,omitempty"`
}
type budgets struct {
	RequestLimit   int `json:"request_limit"`
	Samples        int `json:"samples_per_problem"`
	MaxCalls       int `json:"max_calls"`
	MaxTokens      int `json:"max_tokens"`
	MaxCandidates  int `json:"max_candidates"`
	TimeoutSeconds int `json:"timeout_seconds"`
}
type manifest struct {
	SchemaVersion int               `json:"schema_version"`
	Model         compare.Model     `json:"model"`
	References    string            `json:"references"`
	Problems      []compare.Problem `json:"problems"`
	Seed          int64             `json:"seed"`
	Budgets       budgets           `json:"budgets"`
}
type reference struct {
	ID      string `json:"id"`
	Spec    string `json:"spec"`
	Heldout string `json:"heldout"`
	Source  string `json:"source"`
}
type frozenProblem struct {
	problem     tokenspec.Problem
	reference   reference
	id          string
	heldoutPath string
}

// 2026-11-08: 인증 저장소나 비밀 파일은 참조 또는 heldout 입력으로 읽지 않는다.
func readPublicInput(path string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(filepath.ToSlash(resolved))
	name := strings.ToLower(filepath.Base(resolved))
	if filepath.Ext(name) != ".json" || name == "credentials.json" || name == "secrets.json" || name == "token.json" || strings.HasPrefix(name, ".env") {
		return nil, errors.New("protected input")
	}
	for _, part := range strings.Split(lower, "/") {
		if part == ".ssh" || part == ".aws" || part == ".azure" || part == ".kube" || part == "gcloud" {
			return nil, errors.New("protected input")
		}
	}
	return os.ReadFile(resolved)
}

// 2026-11-08: 실험 중 파일 수정이 평가 대상을 바꾸지 않도록 공개 입력을 먼저 고정한다. heldout은 여기서 열지 않는다.
func freeze(path string) (manifest, []frozenProblem, map[string]string, error) {
	var m manifest
	fingerprints := map[string]string{}
	read := func(path, label string) ([]byte, error) {
		b, e := readPublicInput(path)
		if e == nil {
			h := sha256.Sum256(b)
			fingerprints[label] = hex.EncodeToString(h[:])
		}
		return b, e
	}
	data, e := read(path, "manifest")
	if e != nil {
		return m, nil, nil, errors.New("manifest read failed")
	}
	codeFingerprints, e := fingerprintCode()
	if e != nil {
		return m, nil, nil, e
	}
	for k, v := range codeFingerprints {
		fingerprints[k] = v
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil {
		return m, nil, nil, errors.New("invalid manifest")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return m, nil, nil, errors.New("invalid manifest")
	}
	if m.SchemaVersion != 1 || m.Model.Ranker != "jev" || m.Model.Model != "jev-1.14.0" || m.References == "" || len(m.Problems) == 0 || m.Budgets.RequestLimit < 1 || m.Budgets.RequestLimit > 10000 || m.Budgets.Samples < 1 || m.Budgets.Samples > 48 || m.Budgets.MaxCalls < 1 || m.Budgets.MaxCalls > 64 || m.Budgets.MaxTokens < 1 || m.Budgets.MaxTokens > 256 || m.Budgets.MaxCandidates < 1 || m.Budgets.MaxCandidates > 8192 || m.Budgets.TimeoutSeconds < 1 || m.Budgets.TimeoutSeconds > 300 {
		return m, nil, nil, errors.New("invalid manifest settings")
	}
	base := filepath.Dir(path)
	data, e = read(filepath.Join(base, m.References), "references")
	if e != nil {
		return m, nil, nil, errors.New("reference read failed")
	}
	var refs []reference
	if json.Unmarshal(data, &refs) != nil {
		return m, nil, nil, errors.New("invalid references")
	}
	byID := map[string]reference{}
	for _, r := range refs {
		if r.ID == "" || byID[r.ID].ID != "" {
			return m, nil, nil, errors.New("invalid references")
		}
		byID[r.ID] = r
	}
	seen := map[string]bool{}
	out := make([]frozenProblem, 0, len(m.Problems))
	for _, item := range m.Problems {
		r, ok := byID[item.ID]
		if !ok || item.ID == "" || seen[item.ID] || item.Spec == "" || r.Spec != item.Spec {
			return m, nil, nil, errors.New("invalid problem reference")
		}
		seen[item.ID] = true
		b, e := read(filepath.Join(base, item.Spec), "spec:"+item.ID)
		if e != nil {
			return m, nil, nil, errors.New("spec read failed")
		}
		p, e := tokenspec.Decode(strings.NewReader(string(b)))
		if e != nil {
			return m, nil, nil, errors.New("invalid spec")
		}
		heldout := ""
		if item.Heldout != "" {
			heldout, e = filepath.Abs(filepath.Join(base, item.Heldout))
			if e != nil {
				return m, nil, nil, errors.New("invalid heldout path")
			}
		}
		out = append(out, frozenProblem{problem: p, reference: r, id: item.ID, heldoutPath: heldout})
	}
	return m, out, fingerprints, nil
}

// 2026-11-08: reference 이름을 공급기의 v0/v1 규칙으로 정규화하되 모델 입력에는 포함하지 않는다.
func normalize(source string, p tokenspec.Problem) []string {
	names := map[string]string{}
	for _, name := range p.Inputs {
		names[name] = name
	}
	for _, name := range []string{"main", "f0", "int", "bool", "string", "true", "false", "nil", "len", "cap", "append", "copy", "delete", "make", "_"} {
		names[name] = name
	}
	var s scanner.Scanner
	s.Init(token.NewFileSet().AddFile("ref.go", -1, len(source)), []byte(source), nil, 0)
	var out []string
	next := 0
	for {
		_, kind, lit := s.Scan()
		if kind == token.EOF {
			break
		}
		lex := lit
		if lex == "" {
			lex = kind.String()
		}
		if kind == token.SEMICOLON {
			lex = ";"
		}
		if kind == token.IDENT {
			if v, ok := names[lex]; ok {
				lex = v
			} else {
				for {
					v := fmt.Sprintf("v%d", next)
					next++
					if _, used := names[v]; !used {
						names[lex] = v
						names[v] = v
						lex = v
						break
					}
				}
			}
		}
		out = append(out, lex)
	}
	if len(out) > 0 && out[len(out)-1] == ";" {
		out = out[:len(out)-1]
	}
	return out
}

type script struct {
	tokens    []string
	index     int
	reason    string
	position  int
	captures  []capturedDecision
	rejection string
}
type capturedDecision struct {
	State      string
	Candidates []rank.Candidate
	ValidIDs   []string
}

func (s *script) Rank(_ context.Context, state string, cs []rank.Candidate) ([]rank.Score, error) {
	var data struct {
		Parents map[string]struct {
			Prefix string `json:"prefix"`
		} `json:"parents"`
	}
	if json.Unmarshal([]byte(state), &data) != nil || len(data.Parents) != 1 {
		s.reason = "invalid_search_state"
		return nil, errors.New("replay blocked")
	}
	for _, p := range data.Parents {
		if p.Prefix != strings.Join(s.tokens[:s.index], " ") {
			s.reason = "prefix_mismatch"
			s.position = s.index
			return nil, errors.New("replay blocked")
		}
	}
	if s.index >= len(s.tokens) {
		s.reason = "reference_exhausted"
		return nil, errors.New("replay blocked")
	}
	scores := make([]rank.Score, len(cs))
	found := false
	for i, c := range cs {
		scores[i] = rank.Score{ID: c.ID, Score: 0}
		if c.Lexeme == s.tokens[s.index] && !found {
			scores[i].Score = 1
			found = true
		}
	}
	if !found {
		s.reason = "unreachable"
		if s.rejection != "" {
			s.reason = s.rejection
		}
		s.position = s.index
		return nil, errors.New("replay blocked")
	}
	valid := []string{}
	for _, c := range cs {
		if c.Lexeme == s.tokens[s.index] {
			valid = append(valid, c.ID)
		}
	}
	s.captures = append(s.captures, capturedDecision{State: state, Candidates: append([]rank.Candidate(nil), cs...), ValidIDs: valid})
	s.index++
	s.rejection = ""
	return scores, nil
}
func (s *script) trace(e tokensynth.Event) {
	if (e.Reason == "body_rejected" || e.Reason == "declaration_rejected") && s.index < len(s.tokens) && e.Lexeme == s.tokens[s.index] && s.rejection == "" {
		s.rejection = e.Reason + ":" + string(e.RejectionReason)
	}
	if e.Reason == "forced" {
		if s.index >= len(s.tokens) || e.Lexeme != s.tokens[s.index] {
			if s.reason == "" {
				s.reason = "forced_mismatch"
				s.position = s.index
			}
		} else {
			s.index++
			s.rejection = ""
		}
	}
}

// 2026-11-08: 실제 탐색의 필터와 강제 진행을 통과한 참조만 후속 실험의 근거로 삼는다.
func Inspect(ctx context.Context, path string, progress func(Progress)) (Report, error) {
	return inspect(ctx, path, progress, nil)
}
func inspect(ctx context.Context, path string, progress func(Progress), capture func(string, []capturedDecision)) (Report, error) {
	report := Report{SchemaVersion: 1, EvaluationKind: "reference_inspection", Status: "input_error"}
	if err := ctx.Err(); err != nil {
		report.Status = "cancelled"
		report.Summary = "참조 검증이 취소되었습니다."
		return report, err
	}
	m, ps, fp, e := freeze(path)
	if e != nil {
		return report, e
	}
	return inspectFrozen(ctx, m, ps, fp, progress, capture)
}
func inspectFrozen(ctx context.Context, m manifest, ps []frozenProblem, fp map[string]string, progress func(Progress), capture func(string, []capturedDecision)) (Report, error) {
	report := Report{SchemaVersion: 1, EvaluationKind: "reference_inspection"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				report.BuildRevision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				report.BuildModified = setting.Value
			}
		}
	}
	report.RequestLimit = m.Budgets.RequestLimit
	report.Fingerprints = fp
	report.Status = "completed"
	for i, p := range ps {
		if e := ctx.Err(); e != nil {
			report.Status = "cancelled"
			report.Summary = "참조 검증이 취소되었습니다."
			return report, e
		}
		if progress != nil {
			progress(Progress{Phase: "reference", ProblemID: p.id, Completed: i, Total: len(ps), RequestLimit: report.RequestLimit})
		}
		item := ProblemResult{ID: p.id, Status: "blocked"}
		ok, _, err := tokenverify.Check(ctx, p.problem, p.reference.Source, p.problem.PublicTests)
		if ctx.Err() != nil {
			report.Status = "cancelled"
			report.Summary = "참조 검증이 취소되었습니다."
			return report, ctx.Err()
		}
		if err != nil || !ok {
			item.Reason = "reference_public_validation_failed"
		} else {
			s := &script{tokens: normalize(p.reference.Source, p.problem)}
			result, err := tokensynth.Search(ctx, p.problem, s, tokensynth.Options{Strategy: "strict-greedy", MaxCalls: 1000, MaxTokens: 1000, MaxCandidates: 100000, Trace: s.trace}, func(string) (bool, error) { return s.reason == "" && s.index == len(s.tokens), nil })
			item.Calls = result.Calls
			item.Tokens = result.GeneratedTokens
			item.Forced = result.ForcedSteps
			item.ExceedsGenerationBudget = result.Calls > m.Budgets.MaxCalls || result.GeneratedTokens > m.Budgets.MaxTokens || result.Candidates > m.Budgets.MaxCandidates
			item.Position = s.position
			if s.reason != "" {
				item.Reason = s.reason
			} else if err != nil {
				item.Reason = "search_blocked"
				if s.rejection != "" {
					item.Reason = s.rejection
				}
				item.Position = s.index
			} else {
				item.Status = "reachable"
				if capture != nil {
					capture(p.id, s.captures)
				}
			}
		}
		if item.Status != "reachable" {
			report.Status = "blocked"
		}
		report.Problems = append(report.Problems, item)
		if progress != nil {
			progress(Progress{Phase: "reference", ProblemID: p.id, Completed: i + 1, Total: len(ps), RequestLimit: report.RequestLimit})
		}
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "# 참조 도달성 검증\n\n상태: %s\n\n검사한 문제: %d개\n\n", report.Status, len(report.Problems))
	for _, item := range report.Problems {
		fmt.Fprintf(&summary, "- %s: %s (호출 %d, 토큰 %d, 강제 %d", item.ID, item.Status, item.Calls, item.Tokens, item.Forced)
		if item.Reason != "" {
			fmt.Fprintf(&summary, ", 사유 %s, 위치 %d", item.Reason, item.Position)
		}
		if item.ExceedsGenerationBudget {
			summary.WriteString(", 생성 실험 예산 초과")
		}
		summary.WriteString(")\n")
	}
	report.Summary = summary.String()
	return report, nil
}
func Run(ctx context.Context, path string, resolve func(context.Context, compare.Model) (rank.Ranker, error), progress func(Progress)) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{SchemaVersion: 1, EvaluationKind: "hypothesis", Status: "cancelled", Summary: "평가가 취소되었습니다."}, err
	}
	m, problems, fp, err := freeze(path)
	if err != nil {
		return Report{SchemaVersion: 1, EvaluationKind: "hypothesis", Status: "input_error", Summary: "입력을 확인할 수 없습니다."}, err
	}
	captures := map[string][]capturedDecision{}
	r, err := inspectFrozen(ctx, m, problems, fp, progress, func(id string, c []capturedDecision) { captures[id] = c })
	r.EvaluationKind = "hypothesis"
	if err != nil {
		return r, err
	}
	reachable := map[string]bool{}
	ids := []string{}
	for _, p := range r.Problems {
		if p.Status == "reachable" {
			reachable[p.ID] = true
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		r.Status = "blocked"
		r.Summary += "\n도달 가능한 참조가 없어 모델 평가를 보류했습니다.\n"
		return r, nil
	}
	r.PlannedGeneration = len(ids) * 8
	if err = ctx.Err(); err != nil {
		r.Status = "cancelled"
		appendEvaluationSummary(&r)
		return r, err
	}
	if resolve == nil {
		r.Status = "input_error"
		appendEvaluationSummary(&r)
		return r, errors.New("resolver required")
	}
	model, e := resolve(ctx, m.Model)
	if e != nil || model == nil {
		if ctx.Err() != nil {
			r.Status = "cancelled"
			appendEvaluationSummary(&r)
			return r, ctx.Err()
		}
		r.Status = "service_error"
		r.Summary += "\n모델 준비에 실패했습니다.\n"
		appendEvaluationSummary(&r)
		return r, nil
	}
	g := &guardedRanker{inner: model, limit: m.Budgets.RequestLimit}
	b, e := runB(ctx, captures, ids, m, g, progress)
	r.Ranking = &b
	r.RequestsUsed = g.used
	r.ActualModel = g.actualModel
	r.Usage = g.usage()
	if e != nil {
		return finishFailure(ctx, r, e)
	}
	eligible := make([]frozenProblem, 0, len(ids))
	for _, p := range problems {
		if reachable[p.id] {
			eligible = append(eligible, p)
		}
	}
	generated, e := runGeneration(ctx, eligible, g, m.Budgets, progress)
	r.Generation = generated
	r.RequestsUsed = g.used
	r.ActualModel = g.actualModel
	r.Usage = g.usage()
	if e != nil {
		return finishFailure(ctx, r, e)
	}
	if len(ids) != len(problems) {
		r.Status = "blocked"
	} else {
		r.Status = "completed"
	}
	appendEvaluationSummary(&r)
	return r, nil
}

func finishFailure(ctx context.Context, r Report, err error) (Report, error) {
	if ctx.Err() != nil {
		r.Status = "cancelled"
		r.Summary += "\n평가가 취소되어 부분 결과를 보존했습니다.\n"
		appendEvaluationSummary(&r)
		return r, ctx.Err()
	}
	if errors.Is(err, errRequestBudget) {
		r.Status = "budget_exhausted"
		r.Summary += "\n모델 요청 예산이 소진되어 부분 결과를 보존했습니다.\n"
		appendEvaluationSummary(&r)
		return r, nil
	}
	if errors.Is(err, errModelMismatch) {
		r.Status = "model_mismatch"
		r.Summary += "\n응답 모델이 고정 모델과 달라 중단했습니다.\n"
		appendEvaluationSummary(&r)
		return r, nil
	}
	r.Status = "service_error"
	r.Summary += "\n모델 서비스가 실패하여 부분 결과를 보존했습니다.\n"
	appendEvaluationSummary(&r)
	return r, nil
}
