package repository

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type RelationshipEvent struct {
	ID          int64     `db:"id"`
	UserID      string    `db:"user_id"`
	CharacterID string    `db:"character_id"`
	Summary     string    `db:"summary"`
	Weight      float64   `db:"weight"`
	CreatedAt   time.Time `db:"created_at"`
}

type ConversationOutlier struct {
	ConversationID string  `db:"conversation_id"`
	Summary        string  `db:"summary"`
	Distance       float64 `db:"distance"`
}

// GetTopRelationshipEvents は重み上位N件を取得する（常時注入用）
func (r *MemoryRepository) GetTopRelationshipEvents(limit int) ([]RelationshipEvent, error) {
	var events []RelationshipEvent
	err := r.db.Select(&events, `
		SELECT id, user_id, character_id, summary, weight, created_at
		FROM relationship_events
		WHERE user_id = $1 AND character_id = $2
		ORDER BY weight DESC
		LIMIT $3`, r.UserID, r.CharacterID, limit)
	return events, err
}

// SearchRelationshipEvents はembeddingで類似イベントを検索する（検索注入用）
func (r *MemoryRepository) SearchRelationshipEvents(embedding []float64, limit int, threshold float64) ([]RelationshipEvent, error) {
	var events []RelationshipEvent
	err := r.db.Select(&events, `
		SELECT id, user_id, character_id, summary, weight, created_at
		FROM relationship_events
		WHERE user_id = $1 AND character_id = $2
		AND 1 - (embedding <=> $3::vector) >= $4
		ORDER BY embedding <=> $3::vector
		LIMIT $5`, r.UserID, r.CharacterID, toEmbeddingStr(embedding), threshold, limit)
	return events, err
}

// InsertRelationshipEvent は新規イベントを挿入し、maxEventsを超えたら重み最下位を削除する
func (r *MemoryRepository) InsertRelationshipEvent(summary string, weight float64, embedding []float64, maxEvents int) error {
	_, err := r.db.Exec(`
		INSERT INTO relationship_events (user_id, character_id, summary, weight, embedding)
		VALUES ($1, $2, $3, $4, $5::vector)`,
		r.UserID, r.CharacterID, summary, weight, toEmbeddingStr(embedding))
	if err != nil {
		return err
	}

	// 上限超過時に重み最下位のものを削除
	_, err = r.db.Exec(`
		DELETE FROM relationship_events
		WHERE user_id = $1 AND character_id = $2
		AND id NOT IN (
			SELECT id FROM relationship_events
			WHERE user_id = $1 AND character_id = $2
			ORDER BY weight DESC
			LIMIT $3
		)`, r.UserID, r.CharacterID, maxEvents)
	return err
}

// GetNormalCentroid は直近N件のconversation_topics embeddingの平均ベクトルを返す
func (r *MemoryRepository) GetNormalCentroid(limit int) ([]float64, error) {
	var embStrs []string
	err := r.db.Select(&embStrs, `
		SELECT ct.embedding::text
		FROM conversation_topics ct
		JOIN conversations c ON c.id = ct.conversation_id
		WHERE c.user_id = $1
		AND c.character_id = $2
		AND ct.embedding IS NOT NULL
		ORDER BY ct.date DESC
		LIMIT $3`, r.UserID, r.CharacterID, limit)
	if err != nil || len(embStrs) == 0 {
		return nil, err
	}

	centroid := make([]float64, 1536)
	count := 0
	for _, s := range embStrs {
		vec, err := parseEmbeddingStr(s)
		if err != nil || len(vec) != 1536 {
			continue
		}
		for i, v := range vec {
			centroid[i] += v
		}
		count++
	}
	if count == 0 {
		return nil, nil
	}
	for i := range centroid {
		centroid[i] /= float64(count)
	}
	return centroid, nil
}

// GetOutlierConversations は通常時セントロイドから遠い会話を取得する（重要イベント候補）
func (r *MemoryRepository) GetOutlierConversations(centroid []float64, distanceThreshold float64, limit int) ([]ConversationOutlier, error) {
	var results []ConversationOutlier
	err := r.db.Select(&results, `
		SELECT ct.conversation_id::text, ct.summary,
		       (ct.embedding <=> $1::vector) AS distance
		FROM conversation_topics ct
		JOIN conversations c ON c.id = ct.conversation_id
		WHERE c.user_id = $2
		AND c.character_id = $3
		AND ct.embedding IS NOT NULL
		AND ct.summary != ''
		AND (ct.embedding <=> $1::vector) > $4
		AND ct.date >= CURRENT_DATE - INTERVAL '7 days'
		ORDER BY distance DESC
		LIMIT $5`,
		toEmbeddingStr(centroid), r.UserID, r.CharacterID, distanceThreshold, limit)
	return results, err
}

// parseEmbeddingStr はpgvectorのテキスト形式 "[0.1,0.2,...]" を []float64 に変換する
func parseEmbeddingStr(s string) ([]float64, error) {
	s = strings.Trim(s, "[]")
	if s == "" {
		return nil, fmt.Errorf("empty embedding string")
	}
	parts := strings.Split(s, ",")
	result := make([]float64, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("parse error at index %d: %v", i, err)
		}
		result[i] = v
	}
	return result, nil
}
