package repository

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

type Topic struct {
	ID       string   `db:"id"`
	Keywords []string `db:"keywords"`
	Summary  string   `db:"summary"`
	Heat     float64  `db:"heat"`
}

// GetOrCreateTopic はembeddingで類似話題を探し、あれば(id, false)、なければ新規作成して(id, true)を返す
func (r *MemoryRepository) GetOrCreateTopic(embedding []float64, threshold float64) (string, bool, error) {
	embeddingStr := toEmbeddingStr(embedding)

	var topicID string
	query := `
		SELECT id FROM topics
		WHERE embedding IS NOT NULL
		  AND character_id = $3
		  AND (embedding <=> $1::vector) < $2
		ORDER BY (embedding <=> $1::vector) ASC
		LIMIT 1`
	err := r.db.Get(&topicID, query, embeddingStr, 1.0-threshold, r.CharacterID)
	if err == nil {
		return topicID, false, nil
	}

	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return "", false, err
	}
	insertQuery := `
		INSERT INTO topics (keywords, summary, heat, embedding, character_id)
		VALUES ('{}', '', 1.0, $1, $2)
		RETURNING id`
	err = r.db.Get(&topicID, insertQuery, embeddingJSON, r.CharacterID)
	if err != nil {
		return "", false, err
	}
	return topicID, true, nil
}

// SearchTopicsByKeywords はキーワードでtopicsを検索する（2段階検索の1段目）
func (r *MemoryRepository) SearchTopicsByKeywords(keywords []string) ([]Topic, error) {
	var topics []Topic
	query := `
		SELECT id, keywords, summary, heat FROM topics
		WHERE keywords && $1
		ORDER BY heat DESC
		LIMIT 3`
	err := r.db.Select(&topics, query, pq.Array(keywords))
	return topics, err
}

// SearchTopicsByEmbedding はembeddingで類似のconversation_topicsを検索する（2段階検索の2段目）
func (r *MemoryRepository) SearchTopicsByEmbedding(embedding []float64, threshold float64) ([]Topic, error) {
	embeddingStr := toEmbeddingStr(embedding)
	var topics []Topic
	query := `
		SELECT id, keywords, summary, heat FROM topics
		WHERE embedding IS NOT NULL
		  AND (embedding <=> $1::vector) < $2
		ORDER BY (embedding <=> $1::vector) ASC
		LIMIT 3`
	err := r.db.Select(&topics, query, embeddingStr, 1.0-threshold)
	return topics, err
}

// LinkConversationToTopic は会話と話題を中間テーブルで紐づける
func (r *MemoryRepository) LinkConversationToTopic(convID, topicID string) error {
	query := `
		INSERT INTO conversation_topics (conversation_id, topic_id, summary, date)
		VALUES ($1, $2, '', CURRENT_DATE)
		ON CONFLICT DO NOTHING`
	_, err := r.db.Exec(query, convID, topicID)
	return err
}

// IncrementHeat は話題の熱量を減衰させてから+1する
func (r *MemoryRepository) IncrementHeat(topicID string) error {
	query := `
		UPDATE topics
		SET heat           = (heat * 0.95) + 1.0,
		    last_active_at = NOW()
		WHERE id = $1`
	_, err := r.db.Exec(query, topicID)
	return err
}

// UpdateTopicEmbedding は既存ベクトルと新しいベクトルの平均を取って話題ベクトルを育てる
func (r *MemoryRepository) UpdateTopicEmbedding(topicID string, newEmbedding []float64) error {
	var existingStr string
	err := r.db.Get(&existingStr, `SELECT embedding::text FROM topics WHERE id = $1`, topicID)
	if err != nil {
		return err
	}

	trimmed := strings.Trim(existingStr, "[]")
	parts := strings.Split(trimmed, ",")
	existing := make([]float64, len(parts))
	for i, p := range parts {
		fmt.Sscanf(strings.TrimSpace(p), "%f", &existing[i])
	}

	if len(existing) != len(newEmbedding) {
		return nil
	}
	avg := make([]float64, len(existing))
	for i := range existing {
		avg[i] = (existing[i] + newEmbedding[i]) / 2.0
	}

	avgJSON, err := json.Marshal(avg)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`UPDATE topics SET embedding = $1 WHERE id = $2`, avgJSON, topicID)
	return err
}

// UpdateTopic はLLMの査定結果でkeywords・summary・heatを更新する
func (r *MemoryRepository) UpdateTopic(topicID string, keywords []string, summary string, heat float64) error {
	query := `
		UPDATE topics
		SET keywords = $1,
		    summary  = $2,
		    heat     = $3
		WHERE id = $4`
	_, err := r.db.Exec(query, pq.Array(keywords), summary, heat, topicID)
	return err
}

// GetActiveTopics はその日アクティブだった話題を取得する（バッチ用）
func (r *MemoryRepository) GetActiveTopics() ([]Topic, error) {
	var topics []Topic
	query := `
		SELECT id, keywords, summary, heat FROM topics
		WHERE last_active_at >= NOW() - INTERVAL '24 hours'
		  AND character_id = $1
		ORDER BY heat DESC`
	err := r.db.Select(&topics, query, r.CharacterID)
	return topics, err
}

// GetMemoriesByTopic は話題に紐づく全トーク履歴を取得する（バッチ用）
func (r *MemoryRepository) GetMemoriesByTopic(topicID string) ([]Memory, error) {
	var memories []Memory
	query := `
		SELECT m.role, m.content
		FROM memories m
		JOIN conversation_topics ct ON ct.conversation_id = m.conversation_id
		WHERE ct.topic_id = $1
		ORDER BY m.id ASC`
	err := r.db.Select(&memories, query, topicID)
	return memories, err
}

// GetTopTopics は熱量上位の話題要約を取得する（プロンプト用）
func (r *MemoryRepository) GetTopTopics(limit int) ([]Topic, error) {
	var topics []Topic
	query := `
		SELECT id, keywords, summary, heat FROM topics
		WHERE summary != ''
		  AND character_id = $2
		ORDER BY heat DESC
		LIMIT $1`
	err := r.db.Select(&topics, query, limit, r.CharacterID)
	return topics, err
}

// DecayAllHeat は全話題の熱量を毎日減衰させる
func (r *MemoryRepository) DecayAllHeat() error {
	_, err := r.db.Exec(`UPDATE topics SET heat = heat * 0.95`)
	return err
}

// toEmbeddingStr はfloat64スライスをpgvector形式の文字列に変換する
func toEmbeddingStr(embedding []float64) string {
	return fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
}