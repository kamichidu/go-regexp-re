package regexp

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/kamichidu/go-regexp-re/internal/ir"
)

//go:embed assets/explain-view.tmpl
var explainAssets embed.FS

type explainViewData struct {
	Pattern               string
	CompilationTime       string
	OverallStrategy       string
	LiteralPrefix         string
	LiteralPrefixComplete bool
	DFA                   *dfaStats
	LiteralMemory         string
	NumGroups             int
	Pass0                 pass0Data
	Pass1                 pass1Data
	Pass2                 *pass2Data
	Pass3                 *pass3Data
	Pass4                 *pass4Data
	SBL                   sblData
}

type dfaStats struct {
	Identity        string
	States          int
	PrimaryTableMem string
	SearchDFA       bool
	SearchDFAStates int
	SearchDFAMem    string
	RecapTablesMem  string
	TotalMemory     string
}

type pass0Data struct {
	LiteralMatcher bool
	Strategies     []strategyData
	Gaze           string
	GazeDetails    []string
	Snap           string
	SnapDetails    []string
}

type strategyData struct {
	Name     string
	Detail   string
	Selected bool
}

type pass1Data struct {
	Engine string
	Goal   string
}

type pass2Data struct {
	Engine  string
	History string
}

type pass3Data struct {
	Method string
}

type pass4Data struct {
	Method string
}

type sblData struct {
	BLabel       string
	STrend       []trendLabel
	LTrend       []trendLabel
	FitnessLogic []fitnessLogic
}

type trendLabel struct {
	Label  string
	Active bool
}

type fitnessLogic struct {
	S       string
	L       string
	Fitness string
	Active  bool
}

type ExplainOptions struct {
	// MaxPatternLength defines the maximum number of characters to display for the pattern.
	// Use -1 for unlimited length. 0 defaults to 80.
	MaxPatternLength int
}

func (re *Regexp) Explain() string {
	return re.ExplainWithOptions(ExplainOptions{})
}

func (re *Regexp) ExplainWithOptions(opts ExplainOptions) string {
	data := re.getExplainViewData(opts)

	tmpl := template.New("explain-view.tmpl").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"len": func(a interface{}) int {
			switch v := a.(type) {
			case []strategyData:
				return len(v)
			case []string:
				return len(v)
			case []trendLabel:
				return len(v)
			case []fitnessLogic:
				return len(v)
			default:
				return 0
			}
		},
	})

	tmpl, err := tmpl.ParseFS(explainAssets, "assets/explain-view.tmpl")
	if err != nil {
		return fmt.Sprintf("Error parsing template: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("Error executing template: %v", err)
	}

	return buf.String()
}

func (re *Regexp) getExplainViewData(opts ExplainOptions) explainViewData {
	s, bVal, l, _, _, _ := re.estimateSBLWithSources()

	displayPattern := re.expr
	displayPattern = strings.ReplaceAll(displayPattern, "\n", "\\n")
	displayPattern = strings.ReplaceAll(displayPattern, "\r", "\\r")

	limit := opts.MaxPatternLength
	if limit == 0 {
		limit = 80
	}
	if limit != -1 {
		runes := []rune(displayPattern)
		if len(runes) > limit {
			displayPattern = string(runes[:limit]) + "..."
		}
	}

	data := explainViewData{
		Pattern:               displayPattern,
		CompilationTime:       re.compileTime.String(),
		OverallStrategy:       re.strategy.String(),
		LiteralPrefix:         string(re.prefix),
		LiteralPrefixComplete: re.complete,
		NumGroups:             re.numSubexp,
		Pass1: pass1Data{
			Engine: "DFA",
			Goal:   "Leftmost MatchEnd & Priority Determination",
		},
	}

	if re.dfa == nil {
		data.Pass1.Engine = "LiteralMatcher (Exact Match)"
	}

	if re.strategy == strategyLiteral {
		data.LiteralMemory = fmt.Sprintf("%d bytes", len(re.prefix))
		data.DFA = &dfaStats{
			Identity: "N/A (Literal Bypass)",
			States:   1,
		}
		data.Pass0 = pass0Data{
			LiteralMatcher: true,
			Gaze:           "Skip (Literal search is sufficient)",
			Snap:           "Skip (Literal search is sufficient)",
		}
	} else if re.dfa != nil {
		stats := &dfaStats{
			Identity:        "Naked (State-space optimized)",
			States:          re.dfa.NumStates(),
			PrimaryTableMem: formatBytes(uint64(re.dfa.NumStates() * 256 * 4)),
			TotalMemory:     formatBytes(re.dfaMemory()),
		}
		if sd := re.dfa.SearchDFA(); sd != nil {
			stats.SearchDFA = true
			stats.SearchDFAStates = sd.NumStates
			stats.SearchDFAMem = formatBytes(uint64(sd.NumStates * 256))
		}
		if re.strategy == strategyExtended {
			stats.RecapTablesMem = formatBytes(re.recapMemory())
		}
		data.DFA = stats

		p0 := pass0Data{
			Gaze: "Skip (No constraints found)",
			Snap: "Skip (No anchors found)",
		}
		if re.primaryAnchor != nil {
			p0.Gaze, p0.GazeDetails = re.getGazeInfo(re.primaryAnchor)
			p0.Snap, p0.SnapDetails = re.getSnapInfo(re.primaryAnchor)
		}

		selected := re.dfa.SearchStrategy()

		// Literal
		litScore := -1
		if re.primaryAnchor != nil && len(re.primaryAnchor.Anchor) > 0 {
			litScore = re.primaryAnchor.Score()
		} else if re.primaryAugmented != nil {
			litScore = re.primaryAnchor.Score()
		}

		litDetail := "Score N/A (No mandatory literal found)"
		if litScore >= 0 {
			pattern := ""
			if re.primaryAugmented != nil {
				pattern = fmt.Sprintf(" (Pattern: %q)", string(re.primaryAugmented.Pattern))
			} else if re.primaryAnchor != nil {
				pattern = fmt.Sprintf(" (Pattern: %q)", string(re.primaryAnchor.Anchor))
			}
			litDetail = fmt.Sprintf("Score %d (Threshold: %d)%s", litScore, ir.MinLiteralScore, pattern)
		}
		p0.Strategies = append(p0.Strategies, strategyData{
			Name: "Literal (SIMD)", Detail: litDetail, Selected: selected == ir.SearchStrategyLiteral,
		})

		// SearchWarp
		warpAvail := ir.CCWarpKernel(re.searchWarp.Kernel) != ir.CCWarpNone
		warpDetail := "Unavailable"
		if warpAvail {
			warpDetail = fmt.Sprintf("Available (Trigger: %s)", explainCCWarp(&re.searchWarp))
		}
		p0.Strategies = append(p0.Strategies, strategyData{
			Name:     "SearchWarp (SWAR)",
			Detail:   warpDetail,
			Selected: selected == ir.SearchStrategySearchWarp,
		})

		// sDFA
		sd := re.dfa.SearchDFA()
		sdDetail := "Unavailable (Construction skipped/failed)"
		if sd != nil {
			sdDetail = fmt.Sprintf("Available (%d states)", sd.NumStates)
		}
		p0.Strategies = append(p0.Strategies, strategyData{
			Name: "SearchDFA (sDFA)", Detail: sdDetail, Selected: selected == ir.SearchStrategySDFA,
		})
		data.Pass0 = p0
	} else {
		// Literal strategy without DFA
		data.DFA = &dfaStats{States: 1}
	}

	if re.strategy == strategyExtended {
		data.Pass2 = &pass2Data{
			Engine:  "Anchored DFA (Precise Forward Scan)",
			History: "32-bit Bit-packed RLE",
		}
		data.Pass3 = &pass3Data{
			Method: "Backward Trace via RecapTable",
		}
		data.Pass4 = &pass4Data{
			Method: "Group Recap (Tag Licking)",
		}
	}

	// SBL Landscape mapping
	sLevel := "Middle"
	if s < 0.4 {
		sLevel = "Low"
	} else if s >= 0.9 {
		sLevel = "High"
	}

	lLevel := "Middle"
	if l > 0.7 {
		lLevel = "High"
	} else if l < 0.4 {
		lLevel = "Low"
	}

	data.SBL = sblData{
		BLabel: explainB(bVal),
		STrend: []trendLabel{
			{Label: "Low", Active: sLevel == "Low"},
			{Label: "Middle", Active: sLevel == "Middle"},
			{Label: "High", Active: sLevel == "High"},
		},
		LTrend: []trendLabel{
			{Label: "Low", Active: lLevel == "Low"},
			{Label: "Middle", Active: lLevel == "Middle"},
			{Label: "High", Active: lLevel == "High"},
		},
	}

	logics := []fitnessLogic{
		{S: "Low   ", L: "High  ", Fitness: "Optimal"},
		{S: "Middle", L: "High  ", Fitness: "Balanced"},
		{S: "Low   ", L: "Middle", Fitness: "Balanced"},
		{S: "High  ", L: "High  ", Fitness: "Sub-optimal"},
		{S: "Low   ", L: "Low   ", Fitness: "Sub-optimal"},
		{S: "High  ", L: "Low   ", Fitness: "Poor"},
	}

	foundMatch := false
	for i := range logics {
		if strings.TrimSpace(logics[i].S) == sLevel && strings.TrimSpace(logics[i].L) == lLevel {
			logics[i].Active = true
			foundMatch = true
		}
	}
	// Default to Balanced if no match in common logic
	if !foundMatch {
		logics = append(logics, fitnessLogic{S: sLevel, L: lLevel, Fitness: "Balanced", Active: true})
	}
	data.SBL.FitnessLogic = logics

	return data
}

func (re *Regexp) getGazeInfo(a *ir.AnchorInfo) (string, []string) {
	if a.SkipGaze {
		return "Skip (Verified by Search)", nil
	}

	var details []string
	if a.HasBeginText {
		details = append(details, "Boundary: Must be at Text Start (^)")
	}
	if a.HasBeginLine {
		details = append(details, "Boundary: Must be at Line Start (^ or \\n)")
	}
	if a.HasEndText {
		details = append(details, fmt.Sprintf("Boundary: Must be at Text End ($) [Distance: %d]", a.MaxDistToEnd))
	}
	if a.HasEndLine {
		details = append(details, fmt.Sprintf("Boundary: Must be at Line End ($ or \\n) [Distance: %d]", a.MaxDistToLineEnd))
	}

	for _, c := range a.Backward {
		label := "Backward"
		if c.IsRepeat {
			label += " (Repeat)"
		}
		details = append(details, fmt.Sprintf("%s: At %d, match %s", label, c.Offset, explainCCWarp(&c.Info)))
	}
	for _, c := range a.Forward {
		label := "Forward"
		if c.IsRepeat {
			label += " (Repeat)"
		}
		details = append(details, fmt.Sprintf("%s: At +%d, match %s", label, c.Offset, explainCCWarp(&c.Info)))
	}

	if len(details) > 0 {
		return "Strict Gaze (Constraint Propagation)", details
	}
	return "Standard Gaze", nil
}

func (re *Regexp) getSnapInfo(a *ir.AnchorInfo) (string, []string) {
	if a.Distance == 0 && a.IsFixed {
		return "Skip (Anchor is at Match Start)", nil
	}

	var label string
	var details []string
	if a.IsFixed {
		label = "Horizon Discovery (Fixed Distance)"
		details = append(details, fmt.Sprintf("Offset: -%d bytes from anchor", a.Distance))
	} else {
		label = "Horizon Discovery (Variable)"
		details = append(details, fmt.Sprintf("Offset: Min -%d bytes from anchor", a.Distance))
		details = append(details, "Method: Reverse-scan for leading repetitions/anchors")
	}
	return label, details
}

func (re *Regexp) dfaMemory() uint64 {
	if re.dfa == nil {
		return 0
	}
	n := uint64(re.dfa.NumStates())
	mem := n * 256 * 4 // Primary transition table
	if sd := re.dfa.SearchDFA(); sd != nil {
		mem += uint64(sd.NumStates * 256)
	}
	if re.strategy == strategyExtended {
		mem += re.recapMemory()
	}
	// Add other metadata (accepting, priorities, etc.)
	mem += n * 20
	return mem
}

func (re *Regexp) recapMemory() uint64 {
	if re.dfa == nil {
		return 0
	}
	tables := re.dfa.RecapTables()
	if len(tables) == 0 {
		return 0
	}
	n := uint64(re.dfa.NumStates())
	// RecapEntry is approx 48-64 bytes
	return uint64(len(tables)) * n * 256 * 48
}

func formatBytes(b uint64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%.2f KiB", float64(b)/1024)
	}
	return fmt.Sprintf("%.2f MiB", float64(b)/(1024*1024))
}

func (re *Regexp) estimateSBLWithSources() (s, bVal, l float64, sSrc, bSrc, lSrc string) {
	// S estimation
	if re.primaryAnchor != nil {
		score := re.primaryAnchor.Score()
		s = 1.0 - (float64(score) / 1000.0)
		if s < 0.01 {
			s = 0.01
		}
		if s > 0.99 {
			s = 0.99
		}
		sSrc = fmt.Sprintf("Primary Anchor Score (%d)", score)
	} else {
		s = 0.5
		sSrc = "No primary anchor found (Neutral estimate)"
	}

	// B estimation
	if re.dfa != nil {
		bVal = float64(re.dfa.NumStates())
		bSrc = "DFA Subset Construction (Total States)"
	} else {
		bVal = 1.0
		bSrc = "Literal-only Bypass"
	}

	// L estimation
	if re.strategy == strategyLiteral {
		l = 0.9
		lSrc = "Fixed Literal SIMD Path"
	} else if re.dfa != nil {
		if re.dfa.SearchStrategy() == ir.SearchStrategySearchWarp || re.dfa.SearchStrategy() == ir.SearchStrategyLiteral {
			l = 0.8
			lSrc = fmt.Sprintf("SearchStrategy: %s", re.dfa.SearchStrategy().String())
		} else {
			l = 0.3
			lSrc = "Random/Complex Trigger Set"
		}
	} else {
		l = 0.5
		lSrc = "Default neutral estimate"
	}

	return
}

func explainB(b float64) string {
	if b < 10 {
		return "Simple"
	}
	if b < 50 {
		return "Moderate"
	}
	return "Complex"
}

func explainS(s float64) string {
	if s < 0.1 {
		return "Sparse"
	}
	if s < 0.4 {
		return "Moderate"
	}
	return "Dense"
}

func explainL(l float64) string {
	if l > 0.7 {
		return "High"
	}
	if l > 0.4 {
		return "Moderate"
	}
	return "Low"
}

func (s matchStrategy) String() string {
	switch s {
	case strategyNone:
		return "None"
	case strategyLiteral:
		return "Literal (0-pass)"
	case strategyFast:
		return "Fast Path (Boundary Discovery Only)"
	case strategyExtended:
		return "Extended Path (Full Multi-Pass TDFA)"
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

func explainCCWarp(info *ir.CCWarpInfo) string {
	kernel := ir.CCWarpKernel(info.Kernel)
	var name string
	switch kernel {
	case ir.CCWarpNone:
		return "None"
	case ir.CCWarpEqual:
		name = fmt.Sprintf("Equal(%q)", byte(info.V0))
	case ir.CCWarpSingleRange:
		name = fmt.Sprintf("Range[%q-%q]", byte(info.V0), byte(info.V1))
	case ir.CCWarpNotSingleRange:
		name = fmt.Sprintf("NotRange[%q-%q]", byte(info.V0), byte(info.V1))
	case ir.CCWarpAnyChar:
		name = "Any"
	case ir.CCWarpASCIIAny:
		name = "ASCIIAny"
	case ir.CCWarpAnyExceptNL:
		name = "AnyExceptNL"
	case ir.CCWarpNotEqual:
		name = fmt.Sprintf("NotEqual(%q)", byte(info.V0))
	case ir.CCWarpEqualSet:
		var chars []string
		if info.Extra != nil {
			for _, v := range *info.Extra {
				chars = append(chars, fmt.Sprintf("%q", byte(v)))
			}
		}
		name = fmt.Sprintf("Set{%s}", strings.Join(chars, ","))
	case ir.CCWarpNotEqualSet:
		var chars []string
		if info.Extra != nil {
			for _, v := range *info.Extra {
				chars = append(chars, fmt.Sprintf("%q", byte(v)))
			}
		}
		name = fmt.Sprintf("NotSet{%s}", strings.Join(chars, ","))
	case ir.CCWarpBitmask:
		name = "Bitmask"
	case ir.CCWarpNotBitmask:
		name = "NotBitmask"
	default:
		name = fmt.Sprintf("Unknown(%d)", kernel)
	}

	if info.Flags&ir.CCWarpFlagIncludeNL != 0 {
		name += "+NL"
	}
	return name
}
