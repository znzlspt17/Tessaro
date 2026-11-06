package tokensynth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"jevcancoding/internal/rank"
	"jevcancoding/internal/tokenspec"
	"jevcancoding/internal/tokenverify"
)

type Event struct {
	Frontier        []Path                        `json:"frontier,omitempty"`
	Phase           string                        `json:"phase,omitempty"`
	Status          string                        `json:"status,omitempty"`
	Calls           int                           `json:"calls,omitempty"`
	Candidates      int                           `json:"candidates,omitempty"`
	ID              string                        `json:"id"`
	ParentID        string                        `json:"parent_id,omitempty"`
	Lexeme          string                        `json:"lexeme,omitempty"`
	TokenKind       string                        `json:"token_kind,omitempty"`
	Rule            string                        `json:"rule,omitempty"`
	Reason          string                        `json:"reason"`
	RejectionReason tokenverify.DeclarationReason `json:"rejection_reason,omitempty"`
	Step            int                           `json:"step"`
	Rank            int                           `json:"rank,omitempty"`
	Score           *float64                      `json:"score,omitempty"`
	Code            string                        `json:"code,omitempty"`
	DecisionContext map[string]any                `json:"decision_context,omitempty"`
}

type Path struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	Rank       int    `json:"rank"`
	TokenCount int    `json:"token_count"`
}
type Options struct {
	Strategy                           string
	MaxTokens, MaxCalls, MaxCandidates int
	Trace                              func(Event)
	TraceCode                          bool
}
type Result struct {
	GeneratedTokens, ForcedSteps, ModelSteps             int
	Source                                               string
	Tokens                                               []string
	Calls, Candidates, ForcedTokens, ModelSelectedTokens int
}

var ErrNoSolution = errors.New("no solution")
var ErrBudget = errors.New("token search budget exhausted")
var ErrCandidateLimit = errors.New("model candidate limit exceeded")

// 2026-11-05: 빈 소스에서 단일 토큰만 추가하며 완성 프로그램을 선택지로 만들지 않는다.
func Search(ctx context.Context, p tokenspec.Problem, r rank.Ranker, o Options, check func(string) (bool, error)) (out Result, err error) {
	return searchFrom(ctx, p, r, o, check, nil)
}
func searchFrom(ctx context.Context, p tokenspec.Problem, r rank.Ranker, o Options, check func(string) (bool, error), initial []*State) (out Result, err error) {
	if err := p.Validate(); err != nil {
		return out, err
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 256
	}
	if o.MaxCalls <= 0 {
		o.MaxCalls = 128
	}
	if o.MaxCandidates <= 0 {
		o.MaxCandidates = 8192
	}
	beam := 1
	if o.Strategy == "beam" {
		beam = 3
	} else if o.Strategy != "greedy" && o.Strategy != "strict-greedy" {
		return out, fmt.Errorf("invalid strategy")
	}
	strict := o.Strategy == "strict-greedy"
	states := initial
	if states == nil {
		root, e := NewState()
		if e != nil {
			return out, e
		}
		states = []*State{root}
	}
	step := 0
	seen := map[string]bool{}
	tokenLimited := false
	var pending struct {
		states  map[string]*State
		choices map[string]Choice
		checks  map[string]tokenverify.DeclarationCheck
	}
	reject := func(id, parent string, c Choice, reason tokenverify.DeclarationReason) {
		if o.Trace != nil {
			o.Trace(Event{ID: id, ParentID: parent, Lexeme: c.Lexeme, TokenKind: c.Kind, Rule: c.Rule, Reason: "declaration_rejected", RejectionReason: reason, Step: step, Calls: out.Calls, Candidates: out.Candidates})
		}
	}
	rejectBody := func(id, parent string, c Choice, reason tokenverify.DeclarationReason) {
		if o.Trace != nil {
			o.Trace(Event{ID: id, ParentID: parent, Lexeme: c.Lexeme, TokenKind: c.Kind, Rule: c.Rule, Reason: "body_rejected", RejectionReason: reason, Step: step, Calls: out.Calls, Candidates: out.Candidates})
		}
	}
	emit := func(id, parent, reason string, c Choice, s *State, score *float64, rankIndex int) {
		if o.Trace == nil {
			return
		}
		event := Event{ID: id, ParentID: parent, Reason: reason, Lexeme: c.Lexeme, TokenKind: c.Kind, Rule: c.Rule, Step: step, Score: score, Rank: rankIndex, Calls: out.Calls, Candidates: out.Candidates}
		if o.TraceCode && s != nil {
			event.Code = s.Source()
		}
		o.Trace(event)
	}
	// 2026-11-05: 표시 경로는 추측하지 않도록 실제 frontier를 한 이벤트로 확정한다.
	frontier := func(paths []*State) {
		if o.Trace == nil {
			return
		}
		snapshot := make([]Path, 0, len(paths))
		for _, s := range paths {
			if s != nil {
				snapshot = append(snapshot, Path{ID: s.ID, ParentID: s.ParentID, Rank: len(snapshot) + 1, TokenCount: len(s.Tokens)})
			}
		}
		o.Trace(Event{ID: "frontier", Reason: "frontier", Step: step, Frontier: snapshot, Calls: out.Calls, Candidates: out.Candidates})
	}
	finish := func(s *State) (bool, error) {
		emit(s.ID, s.ParentID, "validation_started", Choice{}, s, nil, 0)
		ok, e := check(s.Source())
		emit(s.ID, s.ParentID, "validation_finished", Choice{}, s, nil, 0)
		if e != nil {
			return false, e
		}
		if ok {
			out.Source = s.Source()
			out.Tokens = append([]string(nil), s.Tokens...)
			out.ForcedTokens = s.Forced
			out.ModelSelectedTokens = s.Selected
		}
		return ok, nil
	}
	validInitial := make([]*State, 0, len(states))
	for _, state := range states {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		if state == nil {
			continue
		}
		declaration, e := state.checkDeclaration(p)
		if e != nil {
			return out, e
		}
		if declaration.Status == tokenverify.DeclarationRejected {
			reject(state.ID, state.ParentID, Choice{}, declaration.Reason)
			continue
		}
		if reason := tokenverify.CheckBodyPrefix(p, state.Tokens); reason != "" {
			rejectBody(state.ID, state.ParentID, Choice{}, reason)
			continue
		}
		validInitial = append(validInitial, state)
	}
	states = validInitial
	frontier(states)
	for len(states) > 0 {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		step++
		pending.states = map[string]*State{}
		pending.choices = map[string]Choice{}
		pending.checks = map[string]tokenverify.DeclarationCheck{}
		parents := map[string]map[string]any{}
		var candidates []rank.Candidate
		for stateIndex, state := range states {
			for {
				if e := ctx.Err(); e != nil {
					return out, e
				}
				declaration, e := state.checkDeclaration(p)
				if e != nil {
					return out, e
				}
				if declaration.Status == tokenverify.DeclarationRejected {
					reject(state.ID, state.ParentID, Choice{}, declaration.Reason)
					states[stateIndex] = nil
					frontier(states)
					break
				}
				if Complete(state) {
					ok, e := finish(state)
					if e != nil {
						return out, e
					}
					if ok {
						emit(state.ID, "", "public_passed", Choice{}, state, nil, 0)
						frontier([]*State{state})
						return out, nil
					}
					emit(state.ID, "", "public_failed", Choice{}, state, nil, 0)
					states[stateIndex] = nil
					if strict {
						return out, ErrNoSolution
					}
					break
				}
				if len(state.Tokens) >= o.MaxTokens {
					tokenLimited = true
					emit(state.ID, "", "token_limit", Choice{}, nil, nil, 0)
					states[stateIndex] = nil
					break
				}
				rawChoices, rejectedChoices, exhausted, e := candidatesWithin(p, state, o.MaxCandidates-out.Candidates)
				if e != nil {
					return out, e
				}
				for _, rejected := range rejectedChoices {
					if out.Candidates >= o.MaxCandidates {
						return out, ErrBudget
					}
					out.Candidates++
					id := fmt.Sprintf("%08d", out.Candidates)
					reject(id, state.ID, rejected.Choice, rejected.Reason)
				}
				if exhausted {
					out.Candidates += len(rawChoices)
					return out, ErrBudget
				}
				type checkedChoice struct {
					choice Choice
					check  tokenverify.DeclarationCheck
				}
				choices := make([]checkedChoice, 0, len(rawChoices))
				for _, choice := range rawChoices {
					checked, e := state.checkCandidateDeclaration(p, choice)
					if e != nil {
						return out, e
					}
					if checked.Status == tokenverify.DeclarationRejected {
						if out.Candidates >= o.MaxCandidates {
							return out, ErrBudget
						}
						out.Candidates++
						id := fmt.Sprintf("%08d", out.Candidates)
						reject(id, state.ID, choice, checked.Reason)
						continue
					}
					if choice.Lexeme == ";" || choice.Lexeme == "}" || choice.Lexeme == "{" {
						tokens := append(append([]string(nil), state.Tokens...), choice.Lexeme)
						if reason := tokenverify.CheckBodyPrefix(p, tokens); reason != "" {
							if out.Candidates >= o.MaxCandidates {
								return out, ErrBudget
							}
							out.Candidates++
							rejectBody(fmt.Sprintf("%08d", out.Candidates), state.ID, choice, reason)
							continue
						}
					}
					choices = append(choices, checkedChoice{choice, checked})
				}
				if len(choices) == 0 {
					emit(state.ID, "", "no_next_token", Choice{}, nil, nil, 0)
					states[stateIndex] = nil
					frontier(states)
					break
				}
				if len(choices) == 1 {
					if out.Candidates >= o.MaxCandidates {
						return out, ErrBudget
					}
					out.Candidates++
					id := fmt.Sprintf("%08d", out.Candidates)
					next, e := state.Advance(choices[0].choice, id)
					if e != nil {
						return out, e
					}
					next.rememberDeclaration(choices[0].check)
					verified, e := next.checkDeclaration(p)
					if e != nil {
						return out, e
					}
					if verified.Status == tokenverify.DeclarationRejected {
						reject(id, state.ID, choices[0].choice, verified.Reason)
						states[stateIndex] = nil
						frontier(states)
						break
					}
					next.Forced++
					out.GeneratedTokens++
					out.ForcedSteps++
					emit(id, state.ID, "forced", choices[0].choice, next, nil, 1)
					state = next
					states[stateIndex] = next
					frontier(states)
					continue
				}
				parents[state.ID] = map[string]any{"prefix": state.Source(), "visible_symbols": describe(p, state).visible}
				for _, candidate := range choices {
					c := candidate.choice
					if out.Candidates >= o.MaxCandidates {
						return out, ErrBudget
					}
					out.Candidates++
					id := fmt.Sprintf("%08d", out.Candidates)
					pending.states[id] = state
					pending.choices[id] = c
					pending.checks[id] = candidate.check
					candidates = append(candidates, rank.Candidate{ID: id, ParentID: state.ID, TokenKind: c.Kind, Lexeme: c.Lexeme, Rule: c.Rule, Size: len(state.Tokens) + 1})
					emit(id, state.ID, "available", c, nil, nil, 0)
				}
				break
			}
		}
		if len(candidates) == 0 {
			frontier(nil)
			if tokenLimited {
				return out, ErrBudget
			}
			return out, ErrNoSolution
		}
		if len(candidates) > 255 {
			return out, ErrCandidateLimit
		}
		if out.Calls >= o.MaxCalls {
			return out, ErrBudget
		}
		contextData := map[string]any{"intent": p.Intent, "inputs": p.Inputs, "allowed_types": p.AllowedTypes, "literals": p.Literals, "type_hints": p.TypeHints, "output_type_hint": p.OutputTypeHint, "public_tests": p.PublicTests, "parents": parents}
		stateData, _ := json.Marshal(contextData)
		if o.Trace != nil {
			o.Trace(Event{ID: "model", Reason: "decision_context", DecisionContext: contextData, Step: step, Calls: out.Calls, Candidates: out.Candidates})
		}
		out.Calls++
		emit("model", "", "model_started", Choice{}, nil, nil, 0)
		scores, e := r.Rank(ctx, string(stateData), candidates)
		emit("model", "", "model_finished", Choice{}, nil, nil, 0)
		if e != nil {
			return out, e
		}
		ordered, e := rank.Order(candidates, scores)
		if e != nil {
			return out, e
		}
		scoreByID := map[string]float64{}
		for _, s := range scores {
			scoreByID[s.ID] = s.Score
		}
		states = nil
		for i, c := range ordered {
			choice, parent := pending.choices[c.ID], pending.states[c.ID]
			score := scoreByID[c.ID]
			emit(c.ID, parent.ID, "ranked", choice, nil, &score, i+1)
			if strict && i > 0 {
				emit(c.ID, parent.ID, "pruned_strict", choice, nil, &score, i+1)
				continue
			}
			if len(states) >= beam && choice.Lexeme != "}" {
				emit(c.ID, parent.ID, "pruned", choice, nil, &score, i+1)
				continue
			}
			next, e := parent.Advance(choice, c.ID)
			if e != nil {
				return out, e
			}
			next.rememberDeclaration(pending.checks[c.ID])
			verified, e := next.checkDeclaration(p)
			if e != nil {
				return out, e
			}
			if verified.Status == tokenverify.DeclarationRejected {
				reject(c.ID, parent.ID, choice, verified.Reason)
				continue
			}
			next.Selected++
			out.GeneratedTokens++
			out.ModelSteps++
			if seen[next.Source()] {
				emit(c.ID, parent.ID, "duplicate", choice, next, &score, i+1)
				continue
			}
			seen[next.Source()] = true
			if Complete(next) {
				ok, e := finish(next)
				if e != nil {
					return out, e
				}
				if ok {
					emit(c.ID, parent.ID, "public_passed", choice, next, &score, i+1)
					frontier([]*State{next})
					return out, nil
				}
				emit(c.ID, parent.ID, "public_failed", choice, next, &score, i+1)
				if strict {
					return out, ErrNoSolution
				}
				continue
			}
			if len(states) < beam {
				states = append(states, next)
				emit(c.ID, parent.ID, "active", choice, next, &score, i+1)
			} else {
				emit(c.ID, parent.ID, "pruned", choice, next, &score, i+1)
			}
		}
		frontier(states)
	}
	if tokenLimited {
		return out, ErrBudget
	}
	return out, ErrNoSolution
}
