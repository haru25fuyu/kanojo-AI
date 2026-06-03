package functions

import (
	"context"
	"fmt"
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

	// 初回：行がなければ即時生成トリガーになるよう過去時刻で作成
	if _, err := r.GetInnerState(); err != nil {
		_ = r.SaveMoodState("", 0, 0, time.Now().Add(-1*time.Minute))
	}

	for {
		time.Sleep(15 * time.Minute)

		state, _ := r.GetInnerState()
		status, err := r.GetPartnerStatus()
		if err != nil || status == nil {
			continue
		}

		now := time.Now()
		modelBatch := r.GetSetting("model_batch", "gemini-3.1-flash-lite")
		charaData, _ := r.GetCharacter(chara.ID)
		var charaPrompt string
		if charaData != nil {
			charaPrompt = charaData.SystemPrompt
		}
		statusStr := fmt.Sprintf("好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d",
			status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy)

		intervalMin, _ := strconv.Atoi(r.GetSetting("inner_state_interval_minutes", "90"))

		// ── タイマー発火：next_run_at を過ぎていたら更新 ──────────────────────
		if state == nil || now.After(state.NextRunAt) {
			result, err := gemini.GenerateMoodText(context.Background(), modelBatch, charaPrompt, statusStr, now.Hour())
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
			result, err := gemini.GenerateMoodText(context.Background(), modelBatch, charaPrompt, statusStr, now.Hour())
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
