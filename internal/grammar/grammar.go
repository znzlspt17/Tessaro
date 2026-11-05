package grammar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"regexp"
	"sort"
	"strings"
)

type Terminal struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Rule  string `json:"rule"`
}
type part struct {
	symbol   string
	terminal *Terminal
}
type production struct{ parts []part }
type Grammar struct {
	Name     string
	Rules    map[string]json.RawMessage
	Metadata map[string]json.RawMessage
	rules    map[string][]production
	aliases  map[string]string
	next     int
}
type rawNode struct {
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Value   json.RawMessage   `json:"value"`
	Members []json.RawMessage `json:"members"`
	Content json.RawMessage   `json:"content"`
}

func (n rawNode) stringValue() (string, error) {
	var v string
	if err := json.Unmarshal(n.Value, &v); err != nil {
		return "", fmt.Errorf("invalid %s value", n.Type)
	}
	return v, nil
}
func nodeOf(raw json.RawMessage) (rawNode, error) {
	var n rawNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return n, err
	}
	if n.Type == "" {
		return n, fmt.Errorf("missing node type")
	}
	return n, nil
}

var atomic = map[string]bool{"identifier": true, "int_literal": true, "interpreted_string_literal": true, "raw_string_literal": true}

// 2026-11-05: 원본 규칙과 메타데이터를 보존하고 CFG로 정규화하여 별도 Go 문법을 만들지 않는다.
func Load(data []byte) (*Grammar, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var root map[string]json.RawMessage
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	g := &Grammar{Rules: map[string]json.RawMessage{}, Metadata: map[string]json.RawMessage{}, rules: map[string][]production{}, aliases: map[string]string{}}
	if err := json.Unmarshal(root["name"], &g.Name); err != nil || g.Name == "" {
		return nil, fmt.Errorf("missing grammar name")
	}
	if err := json.Unmarshal(root["rules"], &g.Rules); err != nil || len(g.Rules) == 0 {
		return nil, fmt.Errorf("missing grammar rules")
	}
	for k, v := range root {
		if k != "rules" {
			g.Metadata[k] = append(json.RawMessage(nil), v...)
		}
	}
	keys := make([]string, 0, len(g.Rules))
	for k := range g.Rules {
		if strings.HasPrefix(k, "\x00") {
			return nil, fmt.Errorf("invalid rule name")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := g.validate(g.Rules[k]); err != nil {
			return nil, fmt.Errorf("rule %s: %w", k, err)
		}
		if atomic[k] {
			g.rules[k] = []production{{parts: []part{{terminal: &Terminal{Kind: "lexical", Value: k, Rule: k}}}}}
			continue
		}
		if err := g.compile(k, g.Rules[k], k); err != nil {
			return nil, fmt.Errorf("rule %s: %w", k, err)
		}
	}
	for name, ps := range g.rules {
		for _, p := range ps {
			for _, s := range p.parts {
				if s.terminal != nil && s.terminal.Kind == "lexical" && !atomic[s.terminal.Rule] {
					if _, err := regexp.Compile("^(?:" + s.terminal.Value + ")$"); err != nil {
						return nil, fmt.Errorf("unsupported lexical pattern in rule %s: %w", name, err)
					}
				}
				if s.terminal == nil {
					if _, ok := g.rules[s.symbol]; !ok {
						return nil, fmt.Errorf("rule %s references missing %s", name, s.symbol)
					}
				}
			}
		}
	}
	return g, nil
}
func (g *Grammar) validate(raw json.RawMessage) error {
	n, err := nodeOf(raw)
	if err != nil {
		return err
	}
	switch n.Type {
	case "STRING", "PATTERN":
		_, err = n.stringValue()
		return err
	case "SYMBOL":
		if n.Name == "" {
			return fmt.Errorf("empty symbol")
		}
		if _, ok := g.Rules[n.Name]; !ok {
			return fmt.Errorf("unknown symbol %q", n.Name)
		}
		return nil
	case "BLANK":
		return nil
	case "SEQ", "CHOICE":
		if n.Type == "CHOICE" && len(n.Members) == 0 {
			return fmt.Errorf("empty choice")
		}
		for _, m := range n.Members {
			if err := g.validate(m); err != nil {
				return err
			}
		}
		return nil
	case "REPEAT", "REPEAT1", "FIELD", "ALIAS", "PREC", "PREC_LEFT", "PREC_RIGHT", "PREC_DYNAMIC", "TOKEN", "IMMEDIATE_TOKEN":
		return g.validate(n.Content)
	default:
		return fmt.Errorf("unsupported construct %q", n.Type)
	}
}
func (g *Grammar) temp() string { g.next++; return fmt.Sprintf("\x00%d", g.next) }
func (g *Grammar) part(raw json.RawMessage, owner string) (part, error) {
	n, err := nodeOf(raw)
	if err != nil {
		return part{}, err
	}
	switch n.Type {
	case "SYMBOL":
		if n.Name == "" {
			return part{}, fmt.Errorf("empty symbol")
		}
		return part{symbol: n.Name}, nil
	case "STRING":
		v, e := n.stringValue()
		if e != nil {
			return part{}, e
		}
		if v != "" {
			return part{terminal: &Terminal{Kind: "literal", Value: v, Rule: owner}}, nil
		}
	case "PATTERN":
		v, e := n.stringValue()
		if e != nil {
			return part{}, e
		}
		return part{terminal: &Terminal{Kind: "lexical", Value: v, Rule: owner}}, nil
	}
	name := g.temp()
	if err = g.compile(name, raw, owner); err != nil {
		return part{}, err
	}
	return part{symbol: name}, nil
}
func (g *Grammar) compile(name string, raw json.RawMessage, owner string) error {
	n, err := nodeOf(raw)
	if err != nil {
		return err
	}
	add := func(parts ...part) { g.rules[name] = append(g.rules[name], production{parts}) }
	switch n.Type {
	case "SYMBOL", "STRING", "PATTERN":
		if n.Type == "STRING" {
			v, e := n.stringValue()
			if e != nil {
				return e
			}
			if v == "" {
				add()
				return nil
			}
		}
		p, e := g.part(raw, owner)
		if e != nil {
			return e
		}
		add(p)
	case "BLANK":
		add()
	case "SEQ":
		ps := make([]part, 0, len(n.Members))
		for _, m := range n.Members {
			p, e := g.part(m, owner)
			if e != nil {
				return e
			}
			ps = append(ps, p)
		}
		add(ps...)
	case "CHOICE":
		if len(n.Members) == 0 {
			return fmt.Errorf("empty choice")
		}
		for _, m := range n.Members {
			p, e := g.part(m, owner)
			if e != nil {
				return e
			}
			add(p)
		}
	case "REPEAT", "REPEAT1":
		p, e := g.part(n.Content, owner)
		if e != nil {
			return e
		}
		if n.Type == "REPEAT" {
			add()
		} else {
			add(p)
		}
		add(part{symbol: name}, p)
	case "FIELD", "ALIAS", "PREC", "PREC_LEFT", "PREC_RIGHT", "PREC_DYNAMIC":
		if n.Type == "ALIAS" {
			child, e := nodeOf(n.Content)
			if e != nil {
				return e
			}
			if child.Type == "SYMBOL" && child.Name == "identifier" {
				v, e := n.stringValue()
				if e != nil {
					return e
				}
				g.aliases[name] = v
			}
		}
		p, e := g.part(n.Content, owner)
		if e != nil {
			return e
		}
		add(p)
	case "TOKEN", "IMMEDIATE_TOKEN":
		pattern, literal, e := g.tokenPattern(n.Content, map[string]bool{})
		if e != nil {
			return e
		}
		kind := "lexical"
		if literal {
			kind = "literal"
		}
		add(part{terminal: &Terminal{Kind: kind, Value: pattern, Rule: owner}})
	default:
		return fmt.Errorf("unsupported construct %q", n.Type)
	}
	return nil
}
func (g *Grammar) tokenPattern(raw json.RawMessage, visiting map[string]bool) (string, bool, error) {
	n, e := nodeOf(raw)
	if e != nil {
		return "", false, e
	}
	switch n.Type {
	case "STRING":
		s, e := n.stringValue()
		return s, true, e
	case "PATTERN":
		s, e := n.stringValue()
		return s, false, e
	case "BLANK":
		return "", true, nil
	case "SYMBOL":
		if visiting[n.Name] {
			return "", false, fmt.Errorf("recursive lexical rule")
		}
		r, ok := g.Rules[n.Name]
		if !ok {
			return "", false, fmt.Errorf("unknown lexical rule")
		}
		visiting[n.Name] = true
		s, l, e := g.tokenPattern(r, visiting)
		delete(visiting, n.Name)
		return s, l, e
	case "FIELD", "ALIAS", "PREC", "PREC_LEFT", "PREC_RIGHT", "PREC_DYNAMIC", "TOKEN", "IMMEDIATE_TOKEN":
		return g.tokenPattern(n.Content, visiting)
	case "SEQ", "CHOICE":
		var patterns []string
		allLiteral := true
		for _, m := range n.Members {
			s, l, e := g.tokenPattern(m, visiting)
			if e != nil {
				return "", false, e
			}
			if l {
				s = regexp.QuoteMeta(s)
			} else {
				allLiteral = false
			}
			patterns = append(patterns, s)
		}
		sep := ""
		if n.Type == "CHOICE" {
			sep = "|"
			allLiteral = false
		}
		joined := strings.Join(patterns, sep)
		if allLiteral {
			var b strings.Builder
			for _, m := range n.Members {
				s, _, _ := g.tokenPattern(m, visiting)
				b.WriteString(s)
			}
			return b.String(), true, nil
		}
		return "(?:" + joined + ")", false, nil
	case "REPEAT", "REPEAT1":
		s, l, e := g.tokenPattern(n.Content, visiting)
		if e != nil {
			return "", false, e
		}
		if l {
			s = regexp.QuoteMeta(s)
		}
		suffix := "*"
		if n.Type == "REPEAT1" {
			suffix = "+"
		}
		return "(?:" + s + ")" + suffix, false, nil
	}
	return "", false, fmt.Errorf("unsupported lexical construct %q", n.Type)
}
func matches(t Terminal, lexeme string) bool {
	if lexeme == "" {
		return false
	}
	if t.Kind == "literal" {
		return t.Value == lexeme
	}
	if atomic[t.Rule] && t.Value == t.Rule {
		return scanAtomic(t.Rule, lexeme)
	}
	re, err := regexp.Compile("^(?:" + t.Value + ")$")
	return err == nil && re.MatchString(lexeme)
}
func scanAtomic(rule, lexeme string) bool {
	if lexeme == "" {
		return false
	}
	var s scanner.Scanner
	errors := 0
	fset := token.NewFileSet()
	s.Init(fset.AddFile("candidate", -1, len(lexeme)), []byte(lexeme), func(token.Position, string) { errors++ }, 0)
	pos, tok, lit := s.Scan()
	if fset.Position(pos).Offset != 0 || lit != lexeme {
		return false
	}
	_, next, lit2 := s.Scan()
	if next == token.SEMICOLON && lit2 == "\n" {
		_, next, _ = s.Scan()
	}
	if next != token.EOF || errors != 0 {
		return false
	}
	switch rule {
	case "identifier":
		return tok == token.IDENT
	case "int_literal":
		return tok == token.INT
	case "interpreted_string_literal":
		return tok == token.STRING && strings.HasPrefix(lexeme, "\"")
	case "raw_string_literal":
		return tok == token.STRING && strings.HasPrefix(lexeme, "`")
	}
	return false
}
