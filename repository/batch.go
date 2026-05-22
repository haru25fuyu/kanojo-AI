package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"go_app/gemini"
	"log"
	"strings"
)

type TopicAssessment struct {
	Keywords  []string `json:"keywords"`
	HeatScore float64  `json:"heat_score"`
	Reason    string   `json:"reason"`
	Summary   string   `json:"summary"`
}

// RunNightlyBatch は深夜バッチのエントリポイント
func (r *MemoryRepository) RunNightlyBatch(modelBatch string) {
	log.Println("深夜バッチ開始")

	if err := r.DecayAllHeat(); err != nil {
		log.Printf("熱量減衰失敗: %v", err)
		return
	}

	topics, err := r.GetActiveTopics()
	if err != nil {
		log.Printf("アクティブ話題取得失敗: %v", err)
		return
	}

	log.Printf("査定対象: %d話題", len(topics))

	for _, topic := range topics {
		memories, err := r.GetMemoriesByTopic(topic.ID)
		if err != nil || len(memories) == 0 {
			continue
		}

		assessment, err := AssessTopic(context.Background(), modelBatch, memories)
		if err != nil || assessment == nil {
			log.Printf("話題[%s]の査定失敗: %v", topic.ID, err)
			continue
		}

		newHeat := topic.Heat + (assessment.HeatScore * 10.0)

		if err := r.UpdateTopic(topic.ID, assessment.Keywords, assessment.Summary, newHeat); err != nil {
			log.Printf("話題[%s]の更新失敗: %v", topic.ID, err)
			continue
		}

		log.Printf("話題[%s] heat=%.2f keywords=%v summary=%s",
			topic.ID, newHeat, assessment.Keywords, assessment.Summary)
	}

	log.Println("深夜バッチ完了")
}

// AssessTopic はトーク履歴をGeminiに渡してkeywords・熱量査定・要約を返す
func AssessTopic(ctx context.Context, model string, memories []Memory) (*TopicAssessment, error) {
	var sb strings.Builder
	for _, m := range memories {
		sb.WriteString(m.Role + ": " + m.Content + "\n")
	}

	messages := []gemini.Message{
		{
			Role: "system",
			Content: `あなたは会話の分析AIです。
与えられた会話履歴を分析し、以下のJSONのみを返してください。他の文字は一切出力しないでください。

{
  "keywords": ["キーワード1", "キーワード2", "キーワード3"],
  "heat_score": 0.0から1.0の数値（会話の感情的な濃さ・重要度。雑談=0.1、感情的な話題=0.8〜1.0）,
  "reason": "査定理由を一言で",
  "summary": "この話題を一文で要約（日本語）"
}`,
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	rawResponse, err := gemini.GetChatResponseWithContext(ctx, model, messages)
	if err != nil {
		return nil, err
	}

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "{")
	end := strings.LastIndex(rawResponse, "}")
	if start == -1 || end == -1 {
		return nil, fmt.Errorf("JSONが見つかりません: %s", rawResponse)
	}

	var assessment TopicAssessment
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &assessment); err != nil {
		return nil, err
	}

	return &assessment, nil
}

// SummarizeConversation は終了したconversationを要約してconversation_topicsに保存する
func (r *MemoryRepository) SummarizeConversation(modelBatch string, convID string, topicID string) error {
	memories, err := r.GetRecentMemories(convID, 100)
	if err != nil || len(memories) == 0 {
		return err
	}

	assessment, err := AssessTopic(context.Background(), modelBatch, memories)
	if err != nil || assessment == nil {
		return err
	}

	query := `
		UPDATE conversation_topics
		SET summary = $1
		WHERE conversation_id = $2 AND topic_id = $3`
	_, err = r.db.Exec(query, assessment.Summary, convID, topicID)
	return err
}