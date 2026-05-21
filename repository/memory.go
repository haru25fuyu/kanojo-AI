package repository

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

type MemoryRepository struct {
	db *sqlx.DB
}

func NewMemoryRepository(db *sqlx.DB) *MemoryRepository {
	return &MemoryRepository{db: db}
}

// GetOrCreateConversationID は「平均値判定」を行い、適切な会話IDを返す
func (r *MemoryRepository) GetOrCreateConversationID(embedding []float64, avgThreshold float64, maxThreshold float64) (string, error) {
	var convID string
	// pgvectorの形式に合わせてベクトルを文字列化
	embeddingStr := fmt.Sprintf("[%s]", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(embedding)), ","), "[]"))
	
	query := `SELECT get_or_create_conversation_id($1::vector, $2, $3)`
	err := r.db.Get(&convID, query, embeddingStr, avgThreshold, maxThreshold)
	if err != nil {
		return "", err
	}
	return convID, nil
}

// SaveMemory は発言と、判定された会話IDをDBに保存する
func (r *MemoryRepository) SaveMemory(content string, embedding []float64, role string, convID string) error {
	var embeddingJSON interface{}
	if embedding != nil {
		var err error
		embeddingJSON, err = json.Marshal(embedding)
		if err != nil {
			return err
		}
	}

	query := `INSERT INTO memories (content, embedding, role, conversation_id) 
              VALUES ($1, $2, $3, $4)`
	_, err := r.db.Exec(query, content, embeddingJSON, role, convID)
	return err
}

// Memory は取得用の構造体
type Memory struct {
    Role    string `db:"role"`
    Content string `db:"content"`
}

// GetRecentMemories は指定された会話IDに紐づく直近のやり取りを取得する
func (r *MemoryRepository) GetRecentMemories(convID string, limit int) ([]Memory, error) {
    var memories []Memory
    
    // 最新のlimit件を取得しつつ、表示は古い順（時系列）にするためのサブクエリ
    query := `
        SELECT role, content FROM (
            SELECT role, content, id 
            FROM memories 
            WHERE conversation_id = $1 
            ORDER BY id DESC 
            LIMIT $2
        ) AS recent 
        ORDER BY id ASC`
    
    err := r.db.Select(&memories, query, convID, limit)
    if err != nil {
        return nil, err
    }
    return memories, nil
}

func (r *MemoryRepository) GetSetting(key string, defaultValue string) string {
    var value string
    query := `SELECT value FROM settings WHERE key = $1`
    err := r.db.Get(&value, query, key)
    if err != nil {
        return defaultValue
    }
    return value
}

