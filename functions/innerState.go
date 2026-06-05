package functions

import (
	"context"
	"go_app/gemini"
	"go_app/repository"
	"log"
)

func updateInnerState(r *repository.MemoryRepository, repo *repository.MemoryRepository, chara repository.Character) {
	status, err := r.GetPartnerStatus()
	if err != nil || status == nil {
		return
	}

	modelBatch := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")

	rawStages, _ := r.GetCharacterStages(chara.ID)
	var stagePrompt string
	if len(rawStages) > 0 {
		entries := make([]gemini.StageEntry, len(rawStages))
		for i, s := range rawStages {
			e := gemini.StageEntry{
				Parameter: s.Parameter,
				StageFrom: s.StageFrom,
				StageTo:   s.StageTo,
				Prompt:    s.Prompt,
			}
			if s.FilterParam != nil {
				e.FilterParam = *s.FilterParam
				e.FilterFrom = *s.FilterFrom
				e.FilterTo = *s.FilterTo
			}
			entries[i] = e
		}
		stagePrompt = gemini.ResolveStagePrompt(entries, map[string]int{
			"trust":     status.Trust,
			"affection": status.Affection,
			"fatigue":   status.Fatigue,
			"mood":      status.Mood,
			"stress":    status.Stress,
			"energy":    status.Energy,
		})
	}

	result, err := gemini.GenerateMoodText(context.Background(), modelBatch, stagePrompt)
	if err != nil || result == nil {
		log.Printf("[内面] 生成失敗: %v", err)
		return
	}
	_ = r.SaveMoodOnly(result.MoodText, status.Mood, status.Stress)
	log.Printf("[内面] 更新 user=%s chara=%s: %q", r.UserID, chara.ID, result.MoodText)
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
