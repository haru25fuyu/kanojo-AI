package functions

import (
    "context"
    "go_app/gemini"
    "go_app/repository"
    "log"
    "math/rand"
    "strconv"
    "time"
)

func RunEventLoop(repo *repository.MemoryRepository, chara repository.Character) {
    r := repo.WithIDs("default", chara.ID)

    // 起動後1時間半待機
    time.Sleep(90 * time.Minute)

    for {
        // 乱数デルタ適用
        randomDelta := repository.StatusDelta{
            Fatigue: rand.Intn(3000) - 500,
            Mood:    rand.Intn(6000) - 3000,
            Stress:  rand.Intn(2000) - 500,
            Energy:  rand.Intn(4000) - 2000,
        }
        if err := r.ApplyStatusDelta(randomDelta); err != nil {
            log.Printf("乱数デルタ適用失敗: %v", err)
        }
        log.Printf("乱数デルタ適用: fatigue=%d mood=%d stress=%d energy=%d",
            randomDelta.Fatigue, randomDelta.Mood, randomDelta.Stress, randomDelta.Energy)

        // 夜間はinner_state更新スキップ
        hour := time.Now().Hour()
        hourStart, _ := strconv.Atoi(repo.GetSetting("proactive_hour_start", "8"))
        hourEnd, _ := strconv.Atoi(repo.GetSetting("proactive_hour_end", "22"))
        if hour >= hourStart && hour < hourEnd {
            updateInnerState(r, repo, chara)
        }

        time.Sleep(90 * time.Minute)
    }
}

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
    log.Printf("[内面] 更新: %q", result.MoodText)
}

func iabs(x int) int {
    if x < 0 {
        return -x
    }
    return x
}