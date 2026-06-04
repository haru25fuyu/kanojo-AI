package functions

import (
	"context"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"strconv"
)

// SeedCharaInfoFromPrompt はキャラクターの system_prompt から事実情報を抽出して
// chara_info に保存する。起動時に1回だけ呼ぶ想定。
// 既存キーとの dedup も行うので重複は自動統合される。
func SeedCharaInfoFromPrompt(repo *repository.MemoryRepository, chara repository.Character) {
	r := repo.WithIDs("default", chara.ID)
	modelBatch := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")
	dedupThreshold, _ := strconv.ParseFloat(repo.GetSetting("chara_info_dedup_threshold", "0.90"), 64)

	if chara.SystemPrompt == "" {
		log.Printf("[seed] キャラ[%s] system_prompt が空のためスキップ", chara.ID)
		return
	}

	if chara.CharaInfoSeeded {
		log.Printf("[seed] キャラ[%s] 済みのためスキップ", chara.ID)
		return
	}

	items, err := gemini.ExtractCharaInfoFromPrompt(context.Background(), modelBatch, chara.SystemPrompt)
	if err != nil {
		log.Printf("[seed] キャラ[%s] 抽出失敗: %v", chara.ID, err)
		return
	}
	if len(items) == 0 {
		log.Printf("[seed] キャラ[%s] 抽出結果が空", chara.ID)
		return
	}

	saved := 0
	for _, item := range items {
		emb := gemini.GetEmbedding(item.Key + ": " + item.Value)
		// 既存キーと照合（dedup）
		existing, _ := r.SearchCharaInfo(emb, 1, dedupThreshold, 9999)
		key := item.Key
		if len(existing) > 0 {
			key = existing[0].Key
		}
		if err := r.UpsertCharaInfo(key, item.Value, item.Importance, emb); err != nil {
			log.Printf("[seed] キャラ[%s] 保存失敗 key=%s: %v", chara.ID, key, err)
			continue
		}
		saved++
	}

	r.DB().Exec(`UPDATE characters SET chara_info_seeded = TRUE WHERE id = $1`, chara.ID)

	log.Printf("[seed] キャラ[%s] 完了: %d件抽出 → %d件保存", chara.ID, len(items), saved)
}
