package gemini

import "strings"

type StageEntry struct {
	Parameter   string
	StageFrom   int
	StageTo     int
	Prompt      string
	FilterParam string // 空文字 = 単一条件
	FilterFrom  int
	FilterTo    int
}

func ResolveStagePrompt(stages []StageEntry, status map[string]int) string {
	seen := map[string]bool{}
	var parts []string
	for _, s := range stages {
		if seen[s.Parameter] {
			continue
		}
		val, ok := status[s.Parameter]
		if !ok || val < s.StageFrom || val > s.StageTo {
			continue
		}
		if s.FilterParam != "" {
			fval, ok := status[s.FilterParam]
			if !ok || fval < s.FilterFrom || fval > s.FilterTo {
				continue
			}
		}
		parts = append(parts, s.Prompt)
		seen[s.Parameter] = true
	}
	return strings.Join(parts, "\n")
}
