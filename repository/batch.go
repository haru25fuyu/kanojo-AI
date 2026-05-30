package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"go_app/gemini"
	"log"
	"math"
	"strings"
	"time"
)

type TopicAssessment struct {
	Keywords  []string `json:"keywords"`
	HeatScore float64  `json:"heat_score"`
	Reason    string   `json:"reason"`
	Summary   string   `json:"summary"`
}

// MergeTopics は類似したtopicを統合する
func (r *MemoryRepository) MergeTopics(threshold float64) error {
	var topics []struct {
		ID      string  `db:"id"`
		Summary string  `db:"summary"`
		Heat    float64 `db:"heat"`
	}
	err := r.db.Select(&topics, `
		SELECT id, summary, heat FROM topics
		WHERE embedding IS NOT NULL
		  AND character_id = $1
		  AND summary != ''
		ORDER BY heat DESC`, r.CharacterID)
	if err != nil || len(topics) < 2 {
		return err
	}

	merged := map[string]bool{}

	for i := 0; i < len(topics); i++ {
		if merged[topics[i].ID] {
			continue
		}
		for j := i + 1; j < len(topics); j++ {
			if merged[topics[j].ID] {
				continue
			}

			var sim float64
			err := r.db.Get(&sim, `
				SELECT 1 - (a.embedding <=> b.embedding)
				FROM topics a, topics b
				WHERE a.id = $1 AND b.id = $2`,
				topics[i].ID, topics[j].ID,
			)
			if err != nil || sim < threshold {
				continue
			}

			r.db.Exec(`
				UPDATE conversation_topics SET topic_id = $1
				WHERE topic_id = $2`, topics[i].ID, topics[j].ID)

			r.db.Exec(`
				UPDATE topics SET heat = heat + $1 WHERE id = $2`,
				topics[j].Heat, topics[i].ID)

			r.db.Exec(`DELETE FROM topics WHERE id = $1`, topics[j].ID)

			merged[topics[j].ID] = true
			log.Printf("topic merge: %s ← %s (sim: %.3f)", topics[i].ID, topics[j].ID, sim)
		}
	}
	return nil
}

func (r *MemoryRepository) RunNightlyBatch(modelBatch string) {
	log.Println("深夜バッチ開始")

	// 睡眠回復処理
	recoveryMode := r.GetSetting("sleep_recovery_mode", "static")
	if recoveryMode == "dynamic" {
		r.applySleepRecoveryDynamic()
	} else {
		r.applySleepRecoveryStatic()
	}

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

	mergeThreshold := 0.85
	if err := r.MergeTopics(mergeThreshold); err != nil {
		log.Printf("topic merge失敗: %v", err)
	} else {
		log.Println("topic merge完了")
	}

	log.Println("深夜バッチ完了")
}

// AssessTopic はトーク履歴をGeminiに渡してkeywords・熱量査定・要約を返す
func AssessTopic(ctx context.Context, model string, memories []Memory) (*TopicAssessment, error) {
	var sb strings.Builder
	for _, m := range memories {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}

	messages := []gemini.Message{
		{
			Role: "system",
			Content: `あなたは会話の分析AIです。
与えられた会話履歴を分析し、以下のJSONのみを返してください。他の文字は一切出力しないでください。

【要約のルール】
- 「アシスタント」「AI」という表現は使わない
- キャラクターを「彼女」「相手」などと表現する
- ユーザー視点の自然な会話の要約にする
- summaryは会話量に応じて1〜7文で書く。何を話したか・どんな雰囲気だったか・感情的なニュアンス・印象的なエピソード・話題の結末や続きがあるかを含める

{
  "keywords": ["キーワード1", "キーワード2", "キーワード3"],
  "heat_score": 0.0から1.0の数値（会話の感情的な濃さ・重要度。雑談=0.1、感情的な話題=0.8〜1.0）,
  "reason": "査定理由を一言で",
  "summary": "この話題を1〜5文で要約（日本語）"
}`,
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	rawResponse, err := gemini.GetChatResponseWithContext(ctx, model, messages, nil)
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
	// 修正: GetRecentMemories → GetAllMemoriesInConversation
	memories, err := r.GetAllMemoriesInConversation(convID)
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

// applySleepRecoveryStatic は固定値で睡眠回復を適用する
func (r *MemoryRepository) applySleepRecoveryStatic() {
	energy := r.getSettingInt("sleep_recovery_static_energy", 5000)
	fatigue := r.getSettingInt("sleep_recovery_static_fatigue", 5000)
	stress := r.getSettingInt("sleep_recovery_static_stress", 2000)

	r.db.Exec(`
		UPDATE partner_status SET
			energy  = LEAST(10000,  energy  + $1),
			fatigue = GREATEST(0,   fatigue - $2),
			stress  = GREATEST(0,   stress  - $3),
			mood    = LEAST(10000,  mood    + 500)
		WHERE character_id = $4`,
		energy, fatigue, stress, r.CharacterID)
	log.Printf("睡眠回復（固定）: energy+%d fatigue-%d stress-%d", energy, fatigue, stress)
}

// applySleepRecoveryDynamic は無会話時間を睡眠時間とみなして動的に回復する
func (r *MemoryRepository) applySleepRecoveryDynamic() {
	var lastMsgTime time.Time
	err := r.db.Get(&lastMsgTime, `
		SELECT created_at FROM memories
		WHERE character_id = $1
		ORDER BY id DESC LIMIT 1`, r.CharacterID)
	if err != nil {
		// 履歴なしの場合は固定回復にフォールバック
		r.applySleepRecoveryStatic()
		return
	}

	// 22時より前に会話が終わってたら22時から睡眠開始とみなす
	sleepStart := lastMsgTime
	if sleepStart.Hour() < 22 {
		sleepStart = time.Date(
			sleepStart.Year(), sleepStart.Month(), sleepStart.Day(),
			22, 0, 0, 0, sleepStart.Location(),
		)
	}

	// 睡眠時間を計算（最大8時間）
	sleepHours := time.Since(sleepStart).Hours()
	sleepHours = math.Min(math.Max(sleepHours, 0), 8)

	// 8時間睡眠を100%として回復量を計算
	ratio := sleepHours / 8.0
	energy := int(ratio * 6000)
	fatigue := int(ratio * 6000)
	stress := int(ratio * 2000)
	mood := int(ratio * 1000)

	r.db.Exec(`
		UPDATE partner_status SET
			energy  = LEAST(10000,  energy  + $1),
			fatigue = GREATEST(0,   fatigue - $2),
			stress  = GREATEST(0,   stress  - $3),
			mood    = LEAST(10000,  mood    + $4)
		WHERE character_id = $5`,
		energy, fatigue, stress, mood, r.CharacterID)
	log.Printf("睡眠回復（動的）: 睡眠%.1f時間 energy+%d fatigue-%d stress-%d mood+%d",
		sleepHours, energy, fatigue, stress, mood)
}

// getSettingInt はsettingsからint値を取得するヘルパー
func (r *MemoryRepository) getSettingInt(key string, defaultValue int) int {
	var value string
	err := r.db.Get(&value, `SELECT value FROM settings WHERE key = $1`, key)
	if err != nil {
		return defaultValue
	}
	var i int
	if _, err := fmt.Sscanf(value, "%d", &i); err != nil {
		return defaultValue
	}
	return i
}
