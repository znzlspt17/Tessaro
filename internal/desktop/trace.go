package desktop

import (
	"encoding/json"
	"fmt"
	"jevcancode/internal/application"
	"jevcancode/internal/synth"
	"jevcancode/internal/tokensynth"
	"strings"
	"unicode/utf16"
)

type TraceEvent struct {
	Seq         uint64
	RunID, Kind string
	Data        []byte
}
type traceNode struct{ parent, lexeme, reason string }

type traceReducer struct {
	s          application.Snapshot
	nodes      map[string]traceNode
	step       int
	last       uint64
	run        string
	trialIndex int
	epoch      int
	group      string
}

func newTraceReducer() *traceReducer {
	return &traceReducer{nodes: map[string]traceNode{}, step: -1, trialIndex: -1}
}

// 2026-11-08: 판단별 상태를 한 번만 유지하고 선택된 이벤트에만 전체 화면을 만든다.
func (r *traceReducer) apply(e TraceEvent, materialize bool) (Frame, bool) {
	s, nodes := r.s, r.nodes
	step, last, run := r.step, r.last, r.run
	trialIndex, epoch, group := r.trialIndex, r.epoch, r.group

	if run == "" {
		run = e.RunID
	}
	if e.RunID != run || e.Seq <= last {
		return Frame{}, false
	}
	last = e.Seq
	s.RunID = run
	s.Seq = e.Seq
	if e.Kind == "comparison" {
		var wrapper struct {
			TrialIndex int `json:"trial_index"`
			Trial      struct {
				Problem string `json:"problem"`
				Model   string `json:"model"`
			} `json:"trial"`
			Event json.RawMessage `json:"event"`
		}
		if json.Unmarshal(e.Data, &wrapper) == nil && len(wrapper.Event) > 0 {
			if trialIndex != wrapper.TrialIndex {
				trialIndex = wrapper.TrialIndex
				epoch++
				group = ""
				s.ChosenID, s.DecisionStatus, s.ForcedID, s.ForcedLexeme = "", "", "", ""
				nodes = map[string]traceNode{}
				s.Frontier = nil
				s.Candidates = nil
				s.DecisionContext = nil
				step = -1
				s.Current = &application.PublicProblem{ID: wrapper.Trial.Problem, Label: wrapper.Trial.Problem}
				s.Step = 0
				s.RejectionReason = ""
				s.BodyRejectionReason = ""
				s.Model = wrapper.Trial.Model
			}
			e.Kind = "search"
			e.Data = wrapper.Event
		}
	}
	switch e.Kind {
	case "problem_started":
		epoch++
		group = ""
		s.ChosenID, s.DecisionStatus, s.ForcedID, s.ForcedLexeme = "", "", "", ""
		nodes = map[string]traceNode{}
		s.Frontier = nil
		s.Candidates = nil
		s.DecisionContext = nil
		s.RejectionReason = ""
		s.BodyRejectionReason = ""
		s.Step = 0
		step = -1
		var p application.PublicProblem
		if decodeDisplayJSON(e.Data, &p) == nil {
			s.Current = &p
		}
	case "report":
		var report map[string]json.RawMessage
		_ = json.Unmarshal(e.Data, &report)
		if _, summary := report["job_kind"]; !summary {
			s.Payload = append(json.RawMessage(nil), e.Data...)
		}
	case "search":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(e.Data, &fields)
		if _, isAST := fields["elapsed_ms"]; isAST {
			var a synth.TraceEvent
			if json.Unmarshal(e.Data, &a) != nil {
				break
			}
			if a.Step != step {
				s.Candidates = nil
				s.Frontier = nil
				step = a.Step
			}
			s.Step = a.Step
			group = fmt.Sprintf("%d:%d", epoch, a.Step)
			found := false
			for i := range s.Candidates {
				c := &s.Candidates[i]
				if c.ID == a.ID {
					c.Reason = a.Reason
					c.Rank = a.Rank
					c.Score = a.Score
					if a.Code != "" {
						c.Code = a.Code
					}
					found = true
					break
				}
			}
			if !found {
				s.Candidates = append(s.Candidates, application.CandidateItem{ID: a.ID, ParentID: a.ParentID, Rank: a.Rank, Score: a.Score, Reason: a.Reason, Code: a.Code})
			}
			if a.Rank > 0 && a.Rank <= 3 && (a.Reason == "ranked" || a.Reason == "active" || a.Reason == "accepted" || a.Reason == "public_passed") {
				found = false
				for i := range s.Frontier {
					if s.Frontier[i].ID == a.ID {
						s.Frontier[i].Code = a.Code
						found = true
						break
					}
				}
				if !found {
					s.Frontier = append(s.Frontier, application.FrontierItem{ID: a.ID, ParentID: a.ParentID, Rank: a.Rank, Code: a.Code})
				}
			}
			break
		}
		var v tokensynth.Event
		if decodeDisplayJSON(e.Data, &v) != nil {
			break
		}
		if v.Step != step && v.Step > 0 && (v.Lexeme != "" || v.Reason == "decision_context") {
			s.Candidates = nil
			s.DecisionContext = nil
			s.ChosenID, s.DecisionStatus, s.ForcedID, s.ForcedLexeme = "", "", "", ""
			step = v.Step
		}
		if v.Step > 0 {
			s.Step = v.Step
			group = fmt.Sprintf("%d:%d", epoch, v.Step)
		}
		if v.Reason == "decision_context" {
			s.ChosenID = ""
			s.DecisionStatus = "prepared"
			s.ForcedID, s.ForcedLexeme = "", ""
		}
		if v.Reason == "model_started" {
			s.ChosenID = ""
			s.DecisionStatus = "calling"
		}
		if v.Reason == "ranked" && v.Score != nil && s.DecisionStatus == "calling" {
			s.DecisionStatus = "received"
		}
		if v.Reason == "decision_result" {
			s.ChosenID = v.ChosenID
			s.DecisionStatus = v.DecisionStatus
		}
		if v.Reason == "forced" {
			s.ForcedID = v.ID
			s.ForcedLexeme = v.Lexeme
			if s.DecisionContext == nil {
				s.DecisionStatus = "forced"
				s.ChosenID = ""
			}
		}
		if v.Reason == "available" && s.DecisionStatus == "forced" {
			s.DecisionStatus = ""
			s.ForcedID, s.ForcedLexeme = "", ""
		}
		if v.Reason == "declaration_rejected" {
			s.RejectionReason = string(v.RejectionReason)
		}
		if v.Reason == "body_rejected" {
			s.BodyRejectionReason = string(v.RejectionReason)
		}
		if v.ID != "" && v.Lexeme != "" {
			old := nodes[v.ID]
			reason := old.reason
			if v.Reason == "forced" || v.Reason == "active" {
				reason = v.Reason
			}
			nodes[v.ID] = traceNode{v.ParentID, v.Lexeme, reason}
		}
		if v.Reason == "decision_context" {
			s.DecisionContext = v.DecisionContext
		}
		isCandidate := v.Lexeme != "" && v.ID != "" && (v.Reason == "available" || v.Reason == "ranked" || v.Reason == "body_rejected" || v.Reason == "declaration_rejected")
		if isCandidate {
			if v.Step != step {
				s.Candidates = nil
				s.DecisionContext = nil
				step = v.Step
			}
			found := false
			for i := range s.Candidates {
				if s.Candidates[i].ID == v.ID {
					found = true
					break
				}
			}
			if !found {
				s.Candidates = append(s.Candidates, application.CandidateItem{ID: v.ID, ParentID: v.ParentID, Lexeme: v.Lexeme, TokenKind: v.TokenKind, Reason: v.Reason, RejectionReason: string(v.RejectionReason), RejectionTokenIndex: v.RejectionTokenIndex, Code: traceCode(nodes, v.ID)})
			}
		}
		for i := range s.Candidates {
			c := &s.Candidates[i]
			if c.ID == v.ID && v.Reason != "available" {
				c.Reason = v.Reason
				if v.RejectionReason != "" {
					c.RejectionReason = string(v.RejectionReason)
				}
				if v.RejectionTokenIndex != nil {
					c.RejectionTokenIndex = v.RejectionTokenIndex
				}
				if v.Rank > 0 {
					c.Rank = v.Rank
				}
				if v.Score != nil {
					c.Score = v.Score
				}
			}
		}
		if v.Reason == "frontier" {
			s.Frontier = nil
			for _, p := range v.Frontier {
				s.Frontier = append(s.Frontier, application.FrontierItem{ID: p.ID, ParentID: p.ParentID, Rank: p.Rank, TokenCount: p.TokenCount, Code: traceCode(nodes, p.ID)})
			}
		}
	case "problem_finished":
		var p application.ProblemResult
		if json.Unmarshal(e.Data, &p) == nil {
			s.Results = append(s.Results, p)
		}
	}
	r.s, r.nodes = s, nodes
	r.step, r.last, r.run = step, last, run
	r.trialIndex, r.epoch, r.group = trialIndex, epoch, group
	return r.frame(materialize), true
}

func (r *traceReducer) frame(materialize bool) Frame {
	s, nodes := r.s, r.nodes
	f := Frame{RunID: r.run, Seq: r.last, Group: r.group, Step: s.Step, DecisionStatus: s.DecisionStatus}
	if s.Current != nil {
		f.ProblemID = s.Current.ID
	}
	if s.DecisionStatus == "forced" {
		f.Choice = s.ForcedLexeme
	} else if s.ChosenID != "" {
		for _, c := range s.Candidates {
			if c.ID == s.ChosenID {
				f.Choice = c.Lexeme
				break
			}
		}
	}
	if !materialize {
		return f
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	markers := map[string]Marker{}
	for _, path := range s.Frontier {
		n, ok := nodes[path.ID]
		if ok && n.lexeme != "" && (n.reason == "forced" || n.reason == "active") && strings.HasSuffix(path.Code, n.lexeme) {
			end := len(utf16.Encode([]rune(path.Code)))
			start := end - len(utf16.Encode([]rune(n.lexeme)))
			markers[path.ID] = Marker{Start: start, End: end, Reason: n.reason}
		}
	}
	f.Text, f.Markers = string(b), markers
	return f
}

func Reconstruct(events []TraceEvent) []Frame {
	var out []Frame
	reducer := newTraceReducer()
	for _, e := range events {
		if f, ok := reducer.apply(e, true); ok {
			out = append(out, f)
		}
	}
	return out
}

func traceCode(nodes map[string]traceNode, id string) string {
	var p []string
	seen := map[string]bool{}
	for id != "" && id != "root" && !seen[id] {
		seen[id] = true
		n, ok := nodes[id]
		if !ok {
			break
		}
		p = append(p, n.lexeme)
		id = n.parent
	}
	for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
	return strings.Join(p, " ")
}
