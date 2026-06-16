package functions

import (
	"context"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"time"
)

const (
	shiftHorizonDays  = 14               // この日数先まで常にシフトを用意する
	shiftRefillDays   = 7                // 残りがこれ未満になったら次のバッチを投げる
	shiftPollInterval = 30 * time.Minute // ポーリング＆補充の間隔
)

// RunShiftScheduleLoop は現実系シフト表を Batch API で先行生成し続けるループ。
//
//  1. 処理中ジョブをポーリングし、完了したらシフトを書き込む
//  2. 現実系モード時、各キャラのバッファが薄ければ不足日ぶんのバッチを投げる
//
// バッファ(14日) > バッチSLO(最大24h) なので、これで枯れない。
// バッチ作成は非冪等なので、処理中ジョブが1本でもあるキャラには再投入しない。
func RunShiftScheduleLoop(repo *repository.MemoryRepository) {
	// 起動直後に1回
	pollShiftJobs(repo)
	refillShifts(repo)

	ticker := time.NewTicker(shiftPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		pollShiftJobs(repo)
		refillShifts(repo)
	}
}

// pollShiftJobs は処理中ジョブを確認し、完了したものを取り込む。
func pollShiftJobs(repo *repository.MemoryRepository) {
	jobs, err := repo.GetPendingShiftBatchJobs()
	if err != nil {
		log.Printf("[シフト] ジョブ取得失敗: %v", err)
		return
	}

	for _, job := range jobs {
		st, err := gemini.PollShiftBatch(context.Background(), job.JobName)
		if err != nil {
			log.Printf("[シフト] ポーリング失敗 job=%s: %v", job.JobName, err)
			continue
		}

		switch st.State {
		case "JOB_STATE_SUCCEEDED":
			r := repo.WithIDs("default", job.CharacterID)
			saved := 0
			for i, res := range st.Results {
				if i >= len(job.Dates) || !res.OK {
					continue
				}
				if err := r.UpsertCharacterShift(job.Dates[i], res.Blocks); err != nil {
					log.Printf("[シフト] 保存失敗 chara=%s date=%s: %v",
						job.CharacterID, job.Dates[i].Format("2006-01-02"), err)
					continue
				}
				saved++
			}
			repo.UpdateShiftBatchJobStatus(job.ID, "done")
			log.Printf("[シフト] 取込完了 chara=%s %d/%d日", job.CharacterID, saved, len(job.Dates))

		case "JOB_STATE_FAILED", "JOB_STATE_CANCELLED", "JOB_STATE_EXPIRED":
			repo.UpdateShiftBatchJobStatus(job.ID, "failed")
			log.Printf("[シフト] ジョブ異常終了 chara=%s state=%s err=%s（次回補充で再投入）",
				job.CharacterID, st.State, st.ErrMsg)

		default:
			// JOB_STATE_PENDING / JOB_STATE_RUNNING → そのまま待つ
		}
	}
}

// refillShifts は現実系モード時、バッファが薄いキャラのシフトを補充する。
func refillShifts(repo *repository.MemoryRepository) {
	if repo.GetSetting("chat_mode", "roleplay") == "roleplay" {
		return // なりきり中はシフト不要
	}

	model := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")
	today := time.Now()

	chars, err := repo.GetActiveCharacters()
	if err != nil {
		log.Printf("[シフト] キャラ取得失敗: %v", err)
		return
	}

	for _, c := range chars {
		r := repo.WithIDs("default", c.ID)

		// 処理中ジョブがあれば二重投入しない（作成は非冪等）
		if pending, _ := r.HasPendingShiftBatchJob(); pending {
			continue
		}

		future, _ := r.CountFutureShiftDays(today)
		if future >= shiftRefillDays {
			continue // バッファ十分
		}

		missing, _ := r.MissingShiftDates(today, shiftHorizonDays)
		if len(missing) == 0 {
			continue
		}

		jobName, err := gemini.SubmitShiftBatch(context.Background(), model, c.Name, c.SystemPrompt, missing)
		if err != nil {
			log.Printf("[シフト] バッチ投入失敗 chara=%s: %v", c.ID, err)
			continue
		}
		if err := r.InsertShiftBatchJob(jobName, missing); err != nil {
			log.Printf("[シフト] ジョブ記録失敗 chara=%s job=%s: %v", c.ID, jobName, err)
			continue
		}
		log.Printf("[シフト] バッチ投入 chara=%s job=%s %d日分（〜%d日先まで）",
			c.ID, jobName, len(missing), shiftHorizonDays)
	}
}
