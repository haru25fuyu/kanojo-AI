package repository

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type MemoryRepository struct {
	db          *sqlx.DB
	UserID      string
	CharacterID string
}

func NewMemoryRepository(db *sqlx.DB) *MemoryRepository {
	return &MemoryRepository{db: db, UserID: "default", CharacterID: "default"}
}

func (r *MemoryRepository) WithIDs(userID, characterID string) *MemoryRepository {
	return &MemoryRepository{
		db:          r.db,
		UserID:      userID,
		CharacterID: characterID,
	}
}

type Memory struct {
	Role           string    `db:"role"`
	Content        string    `db:"content"`
	CreatedAt      time.Time `db:"created_at"`
	ConversationID string    `db:"conversation_id"`
}

func (r *MemoryRepository) DB() *sqlx.DB {
	return r.db
}

func (r *MemoryRepository) GetOrCreateConversationID(embedding []float64, avgThreshold float64, maxThreshold float64) (string, bool, error) {
	embeddingStr := fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
	var result struct {
		ConvID string `db:"conv_id"`
		IsNew  bool   `db:"is_new"`
	}
	query := `SELECT conv_id, is_new FROM get_or_create_conversation_id($1::vector, $2, $3, $4, $5)`
	err := r.db.Get(&result, query, embeddingStr, avgThreshold, maxThreshold, r.UserID, r.CharacterID)
	if err != nil {
		return "", false, err
	}
	return result.ConvID, result.IsNew, nil
}

func (r *MemoryRepository) SaveMemory(content string, embedding []float64, role string, convID string) error {
	var embeddingJSON interface{}
	if embedding != nil {
		var err error
		embeddingJSON, err = json.Marshal(embedding)
		if err != nil {
			return err
		}
	}
	query := `INSERT INTO memories (content, embedding, role, conversation_id, user_id, character_id)
	          VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := r.db.Exec(query, content, embeddingJSON, role, convID, r.UserID, r.CharacterID)
	return err
}

func (r *MemoryRepository) GetRecentMemories(convID string, limit int) ([]Memory, error) {
	var memories []Memory
	query := `
		SELECT role, content, created_at, conversation_id FROM (
			SELECT role, content, created_at, id, conversation_id
			FROM memories
			WHERE user_id = $1 AND character_id = $2
			ORDER BY id DESC
			LIMIT $3
		) AS recent
		ORDER BY id ASC`
	err := r.db.Select(&memories, query, r.UserID, r.CharacterID, limit)
	return memories, err
}

func (r *MemoryRepository) GetAllMemoriesInConversation(convID string) ([]Memory, error) {
	var memories []Memory
	query := `
		SELECT role, content, created_at, conversation_id
		FROM memories
		WHERE conversation_id = $1
		ORDER BY id ASC`
	err := r.db.Select(&memories, query, convID)
	return memories, err
}

func (r *MemoryRepository) GetSetting(key string, defaultValue string) string {
	var value string
	err := r.db.Get(&value, `SELECT value FROM settings WHERE key = $1`, key)
	if err != nil {
		return defaultValue
	}
	return value
}

func (r *MemoryRepository) GetLastMessageTime() (time.Time, error) {
	var t time.Time
	err := r.db.Get(&t, `SELECT created_at FROM memories WHERE user_id = $1 AND character_id = $2 ORDER BY id DESC LIMIT 1`, r.UserID, r.CharacterID)
	return t, err
}

func (r *MemoryRepository) GetLastMemory() (string, error) {
	var content string
	err := r.db.Get(&content, `SELECT content FROM memories WHERE user_id = $1 AND character_id = $2 ORDER BY id DESC LIMIT 1`, r.UserID, r.CharacterID)
	return content, err
}

func (r *MemoryRepository) GetCharacterIDByChannel(channelID string) string {
	var id string
	err := r.db.Get(&id,
		`SELECT id FROM characters WHERE proactive_channel = $1 AND active = TRUE LIMIT 1`,
		channelID,
	)
	if err != nil {
		return "group"
	}
	return id
}

// GetTodayProactiveCount は今日送った自発メッセージの件数を返す
func (r *MemoryRepository) GetTodayProactiveCount() int {
	var count int
	r.db.Get(&count, `
		SELECT COUNT(*) FROM memories
		WHERE character_id = $1
		  AND role = 'proactive'
		  AND created_at >= CURRENT_DATE`,
		r.CharacterID)
	return count
}

// GetTodayConvCount は今日のconversation数を返す
func (r *MemoryRepository) GetTodayConvCount() int {
	var count int
	r.db.Get(&count, `
		SELECT COUNT(DISTINCT conversation_id) FROM memories
		WHERE character_id = $1
		  AND user_id = $2
		  AND created_at >= CURRENT_DATE`,
		r.CharacterID, r.UserID)
	return count
}
