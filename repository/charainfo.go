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
	UpdatedAt    time.Time `db:"updated_at"`
}

// UpsertCharaInfo はキャラクター情報を保存・更新する
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
	_, err = r.db.Exec(query, r.CharacterID, key, value, importance, embeddingJSON)
	return err
}

// GetTopCharaInfo は重要度×頻度スコアで上位N件を取得する
func (r *MemoryRepository) GetTopCharaInfo(limit int) ([]CharaInfo, error) {
	var infos []CharaInfo
	query := `
		SELECT key, value, importance, mention_count, updated_at
		FROM chara_info
		WHERE character_id = $2
		ORDER BY importance * ln(mention_count + 1) DESC
		LIMIT $1`
	err := r.db.Select(&infos, query, limit, r.CharacterID)
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
