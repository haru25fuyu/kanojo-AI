package functions

import (
	"context"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"strconv"
)

// SeedCharaInfoFromPrompt はキャラクターの system_prompt から
// chara_info（事実・背景）と chara_profile（基本情報）を抽出して保存する。
// chara_info_seeded フラグで2回目以降はスキップ。
func SeedCharaInfoFromPrompt(repo *repository.MemoryRepository, chara repository.Character) {
	if chara.SystemPrompt == "" {
		log.Printf("[seed] キャラ[%s] system_prompt が空のためスキップ", chara.ID)
		return
	}
	if chara.CharaInfoSeeded {
		log.Printf("[seed] キャラ[%s] 済みのためスキップ", chara.ID)
		return
	}

	r := repo.WithIDs("default", chara.ID)
	modelBatch := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")
	dedupThreshold, _ := strconv.ParseFloat(repo.GetSetting("chara_info_dedup_threshold", "0.90"), 64)

	// ── chara_profile（名前・年齢・性別・職業）────────────────────────────
	profile, err := gemini.ExtractCharaProfile(context.Background(), modelBatch, chara.SystemPrompt)
	if err != nil {
		log.Printf("[seed] キャラ[%s] chara_profile 抽出失敗: %v", chara.ID, err)
	} else if profile != nil {
		if err := r.UpsertCharaProfile(profile.Name, profile.Age, profile.Gender); err != nil {
			log.Printf("[seed] キャラ[%s] chara_profile 保存失敗: %v", chara.ID, err)
		} else {
			log.Printf("[seed] キャラ[%s] chara_profile 保存完了: name=%s age=%v gender=%s",
				chara.ID, profile.Name, profile.Age, profile.Gender)
		}
	}

	// ── chara_info（事実・背景）──────────────────────────────────────────
	items, err := gemini.ExtractCharaInfoFromPrompt(context.Background(), modelBatch, chara.SystemPrompt)
	if err != nil {
		log.Printf("[seed] キャラ[%s] chara_info 抽出失敗: %v", chara.ID, err)
		return
	}
	if len(items) == 0 {
		log.Printf("[seed] キャラ[%s] chara_info 抽出結果が空", chara.ID)
		return
	}

	saved := 0
	for _, item := range items {
		emb := gemini.GetEmbedding(item.Key + ": " + item.Value)
		existing, _ := r.SearchCharaInfo(emb, 1, dedupThreshold, 9999)
		key := item.Key
		if len(existing) > 0 {
			key = existing[0].Key
		}
		if err := r.UpsertCharaInfo(key, item.Value, item.Importance, emb); err != nil {
			log.Printf("[seed] キャラ[%s] 保存失敗 key=%s: %v", chara.ID, key, err)
			continue
		}
		if item.MinTrust > 0 {
			r.DB().Exec(
				`UPDATE chara_info SET min_trust = $1 WHERE character_id = $2 AND key = $3`,
				item.MinTrust, chara.ID, key,
			)
		}
		if item.MaxTrust > 0 && item.MaxTrust < 10000 {
			r.DB().Exec(
				`UPDATE chara_info SET max_trust = $1 WHERE character_id = $2 AND key = $3`,
				item.MaxTrust, chara.ID, key,
			)
		}
		saved++
	}

	r.DB().Exec(`UPDATE characters SET chara_info_seeded = TRUE WHERE id = $1`, chara.ID)
	log.Printf("[seed] キャラ[%s] 完了: chara_info %d件抽出 → %d件保存", chara.ID, len(items), saved)
}
