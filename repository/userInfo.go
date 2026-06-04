package repository

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type UserInfo struct {
	Key          string    `db:"key"`
	Value        string    `db:"value"`
	Importance   float64   `db:"importance"`
	MentionCount int       `db:"mention_count"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// UpsertUserInfo はユーザー情報を保存・更新する
func (r *MemoryRepository) UpsertUserInfo(key, value string, importance float64, embedding []float64) error {
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO user_info (user_id, character_id, key, value, importance, mention_count, embedding, updated_at)
		VALUES ($1, $2, $3, $4, $5, 1, $6, NOW())
		ON CONFLICT (user_id, character_id, key) DO UPDATE SET
			value         = EXCLUDED.value,
			importance    = EXCLUDED.importance,
			mention_count = user_info.mention_count + 1,
			embedding     = EXCLUDED.embedding,
			updated_at    = NOW()`
	_, err = r.db.Exec(query, r.UserID, r.CharacterID, key, value, importance, embeddingJSON)
	return err
}

// GetTopUserInfo は重要度×頻度スコアで上位N件を取得する
func (r *MemoryRepository) GetTopUserInfo(limit int) ([]UserInfo, error) {
	var infos []UserInfo
	query := `
		SELECT key, value, importance, mention_count, updated_at
		FROM user_info
		WHERE user_id = $2 AND character_id = $3
		ORDER BY importance * ln(mention_count + 1) DESC
		LIMIT $1`
	err := r.db.Select(&infos, query, limit, r.UserID, r.CharacterID)
	return infos, err
}

// SearchUserInfo はembeddingで関連するユーザー情報を検索する
func (r *MemoryRepository) SearchUserInfo(embedding []float64, limit int) ([]UserInfo, error) {
	embeddingStr := fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
	var infos []UserInfo
	query := `
		SELECT key, value, importance, mention_count, updated_at
		FROM user_info
		WHERE embedding IS NOT NULL
		  AND user_id = $3 AND character_id = $4
		ORDER BY (embedding <=> $1::vector) ASC
		LIMIT $2`
	err := r.db.Select(&infos, query, embeddingStr, limit, r.UserID, r.CharacterID)
	return infos, err
}

// Score はユーザー情報のスコアを返す（デバッグ用）
func (u *UserInfo) Score() float64 {
	return u.Importance * math.Log(float64(u.MentionCount)+1)
}

func (r *MemoryRepository) SearchUserInfoByThreshold(embedding []float64, limit int, threshold float64) ([]UserInfo, error) {
	embeddingStr := fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
	var infos []UserInfo
	query := `
        SELECT key, value, importance, mention_count, updated_at
        FROM user_info
        WHERE embedding IS NOT NULL
          AND user_id = $3 AND character_id = $4
          AND (embedding <=> $1::vector) < $2
        ORDER BY (embedding <=> $1::vector) ASC
        LIMIT $5`
	err := r.db.Select(&infos, query, embeddingStr, 1.0-threshold, r.UserID, r.CharacterID, limit)
	return infos, err
}
