package functions

import (
	"context"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"strconv"
	"time"
)

// RunInnerStateLoop はキャラクターの気分テキストを適応的に更新するループ。
// RunEventLoop（乱数デルタ）と並列に goroutine で起動する。
func RunInnerStateLoop(repo *repository.MemoryRepository, chara repository.Character) {
	r := repo.WithIDs("default", chara.ID)

	for {
		time.Sleep(15 * time.Minute)

		state, _ := r.GetInnerState()
		status, err := r.GetPartnerStatus()
		if err != nil || status == nil {
			continue
		}

		now := time.Now()
		modelBatch := r.GetSetting("model_batch", "gemini-3.1-flash-lite")

		// statusStr の代わりに stagePrompt を取得
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
				"trust": status.Trust, "affection": status.Affection,
				"fatigue": status.Fatigue, "mood": status.Mood,
				"stress": status.Stress, "energy": status.Energy,
			})
		}

		intervalMin, _ := strconv.Atoi(r.GetSetting("inner_state_interval_minutes", "90"))

		// ── タイマー発火：next_run_at を過ぎていたら更新 ──────────────────────
		if state == nil || now.After(state.NextRunAt) {
			result, err := gemini.GenerateMoodText(context.Background(), modelBatch, stagePrompt)
			nextRun := now.Add(time.Duration(intervalMin) * time.Minute)
			if err != nil || result == nil {
				log.Printf("[内面] 生成失敗: %v", err)
				mood := ""
				if state != nil {
					mood = state.MoodText
				}
				_ = r.SaveMoodState(mood, status.Mood, status.Stress, nextRun)
				continue
			}
			_ = r.SaveMoodState(result.MoodText, status.Mood, status.Stress, nextRun)
			log.Printf("[内面] 更新: %q  next=%s", result.MoodText, nextRun.Format("15:04"))
			continue
		}

		// ── 変化率トリガー：mood/stress が threshold 以上動いたら即時更新 ─────
		moodThresh, _ := strconv.Atoi(r.GetSetting("mood_trigger_threshold", "1500"))
		stressThresh, _ := strconv.Atoi(r.GetSetting("stress_trigger_threshold", "1000"))
		moodDiff := iabs(status.Mood - state.MoodAtGen)
		stressDiff := iabs(status.Stress - state.StressAtGen)
		if moodDiff >= moodThresh || stressDiff >= stressThresh {
			result, err := gemini.GenerateMoodText(context.Background(), modelBatch, stagePrompt)
			if err == nil && result != nil {
				_ = r.SaveMoodOnly(result.MoodText, status.Mood, status.Stress)
				log.Printf("[内面] 変化トリガー更新: %q (Δmood=%d Δstress=%d)",
					result.MoodText, moodDiff, stressDiff)
			}
		}
	}
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
