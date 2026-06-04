package repository

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type CharaInfo struct {
	Key          string    `db:"key"`
	Value        string    `db:"value"`
	Importance   float64   `db:"importance"`
	MentionCount int       `db:"mention_count"`
	MinTrust     int       `db:"min_trust"` // この値以上の trust でないと取得されない（0=常時）
	MaxTrust     int       `db:"max_trust"` // この値以下の trust でないと取得されない（10000=常時）
	UpdatedAt    time.Time `db:"updated_at"`
}

// UpsertCharaInfo はキャラクター情報を保存・更新する。
// min_trust / max_trust は ON CONFLICT 時に上書きしない（手動設定値を保護する）。
func (r *MemoryRepository) UpsertCharaInfo(key, value string, importance float64, embedding []float64) error {
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO chara_info (character_id, key, value, importance, mention_count, embedding, updated_at)
		VALUES ($1, $2, $3, $4, 1, $5, NOW())
		ON CONFLICT (character_id, key) DO UPDATE SET
			value         = EXCLUDED.value,
			importance    = EXCLUDED.importance,
			mention_count = chara_info.mention_count + 1,
			embedding     = EXCLUDED.embedding,
			updated_at    = NOW()`
	// min_trust / max_trust は EXCLUDED に含めないことで手動設定値を保護
	_, err = r.db.Exec(query, r.CharacterID, key, value, importance, embeddingJSON)
	return err
}

// GetTopCharaInfo は重要度×頻度スコアで上位N件を取得する。
// trust: 現在の信頼度。min_trust > trust または max_trust < trust の項目は除外される。
func (r *MemoryRepository) GetTopCharaInfo(limit int, trust int) ([]CharaInfo, error) {
	var infos []CharaInfo
	query := `
		SELECT key, value, importance, mention_count, min_trust, max_trust, updated_at
		FROM chara_info
		WHERE character_id = $2
		  AND min_trust <= $3
		  AND max_trust >= $3
		ORDER BY importance * ln(mention_count + 1) DESC
		LIMIT $1`
	err := r.db.Select(&infos, query, limit, r.CharacterID, trust)
	return infos, err
}

// SearchCharaInfo は embedding で関連するキャラクター情報を検索する。
// threshold: cosine 類似度の下限（例: 0.45）。これを下回る薄い関連は除外。
// trust: 現在の信頼度。min_trust > trust または max_trust < trust の項目は除外される。
func (r *MemoryRepository) SearchCharaInfo(embedding []float64, limit int, threshold float64, trust int) ([]CharaInfo, error) {
	embeddingStr := toEmbeddingStrChara(embedding)
	var infos []CharaInfo
	query := `
		SELECT key, value, importance, mention_count, min_trust, max_trust, updated_at
		FROM chara_info
		WHERE embedding IS NOT NULL
		  AND character_id = $3
		  AND (embedding <=> $1::vector) < $2
		  AND min_trust <= $4
		  AND max_trust >= $4
		ORDER BY (embedding <=> $1::vector) ASC
		LIMIT $5`
	err := r.db.Select(&infos, query, embeddingStr, 1.0-threshold, r.CharacterID, trust, limit)
	return infos, err
}

// Score はキャラクター情報のスコアを返す
func (c *CharaInfo) Score() float64 {
	return c.Importance * math.Log(float64(c.MentionCount)+1)
}

// toEmbeddingStrChara はfloat64スライスをpgvector形式の文字列に変換する
func toEmbeddingStrChara(embedding []float64) string {
	return fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
}
