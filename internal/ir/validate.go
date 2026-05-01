package ir

import (
	"github.com/kamichidu/go-regexp-re/syntax"
)

// CheckCompatibility performs a static analysis on the optimized AST to ensure
// the pattern is supported by the DFA engine's submatch extraction architecture.
func CheckCompatibility(re *syntax.Regexp) error {
	if re == nil {
		return nil
	}
	switch re.Op {
	case syntax.OpCapture:
		if hasEmptyAlternative(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "empty alternative in capture"}
		}
	case syntax.OpQuest:
		if hasCapture(re.Sub[0]) && matchesEmpty(re.Sub[0]) {
			return &syntax.UnsupportedError{Op: "optional empty capture"}
		}
	}
	for _, sub := range re.Sub {
		if err := CheckCompatibility(sub); err != nil {
			return err
		}
	}
	return nil
}

func hasCapture(re *syntax.Regexp) bool {
	if re.Op == syntax.OpCapture {
		return true
	}
	for _, sub := range re.Sub {
		if hasCapture(sub) {
			return true
		}
	}
	return false
}

func hasEmptyAlternative(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpCapture:
		return hasEmptyAlternative(re.Sub[0])
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if hasEmptyAlternative(sub) {
				return true
			}
		}
	case syntax.OpConcat:
		// If a concat consists only of empty matches, it's an empty alternative
		if len(re.Sub) == 0 {
			return true
		}
		for _, sub := range re.Sub {
			if !hasEmptyAlternative(sub) {
				return false
			}
		}
		return true
	}
	return false
}

func matchesEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpRepeat:
		return re.Min == 0
	case syntax.OpCapture:
		return matchesEmpty(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !matchesEmpty(sub) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if matchesEmpty(sub) {
				return true
			}
		}
		return false
	}
	return false
}

// checkEpsilonLoop identifies patterns that match empty strings in a loop,
// which are fundamentally non-deterministic in a DFA.
func checkEpsilonLoop(prog *syntax.Prog) error {
	visited := make([]bool, len(prog.Inst))
	onStack := make([]bool, len(prog.Inst))

	var dfs func(int) error
	dfs = func(id int) error {
		if onStack[id] {
			return &syntax.UnsupportedError{Op: "epsilon loop"}
		}
		if visited[id] {
			return nil
		}

		visited[id] = true
		onStack[id] = true
		defer func() { onStack[id] = false }()

		inst := prog.Inst[id]
		if isEpsilon(inst.Op) {
			if err := dfs(int(inst.Out)); err != nil {
				return err
			}
			if inst.Op == syntax.InstAlt || inst.Op == syntax.InstAltMatch {
				if err := dfs(int(inst.Arg)); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for i := range prog.Inst {
		if !visited[i] {
			if err := dfs(i); err != nil {
				return err
			}
		}
	}
	return nil
}
