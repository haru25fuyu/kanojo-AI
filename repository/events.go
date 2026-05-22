package repository

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type PartnerEvent struct {
	ID        int64     `db:"id"`
	Event     string    `db:"event"`
	CreatedAt time.Time `db:"created_at"`
}

// SaveEvent はイベントをembeddingと一緒に保存する
func (r *MemoryRepository) SaveEvent(event string, embedding []float64) error {
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(
		`INSERT INTO partner_events (event, embedding, character_id) VALUES ($1, $2, $3)`,
		event, embeddingJSON, r.CharacterID,
	)
	return err
}

// GetRecentEvents は直近N件のイベントを取得する（普段のプロンプト用）
func (r *MemoryRepository) GetRecentEvents(limit int) ([]PartnerEvent, error) {
	var events []PartnerEvent
	err := r.db.Select(&events,
		`SELECT id, event, created_at FROM partner_events WHERE character_id = $2 ORDER BY id DESC LIMIT $1`,
		limit, r.CharacterID,
	)
	return events, err
}

// SearchEvents はembeddingで関連イベントを検索する（質問検知時）
func (r *MemoryRepository) SearchEvents(embedding []float64, limit int) ([]PartnerEvent, error) {
	embeddingStr := fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
	var events []PartnerEvent
	err := r.db.Select(&events, `
		SELECT id, event, created_at FROM partner_events
		WHERE embedding IS NOT NULL
		ORDER BY (embedding <=> $1::vector) ASC
		LIMIT $2`,
		embeddingStr, limit,
	)
	return events, err
}