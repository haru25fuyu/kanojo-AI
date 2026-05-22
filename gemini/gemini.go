package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const baseURL = "https://generativelanguage.googleapis.com/v1beta"

type Message struct {
	Role    string
	Content string
}

type StatusDelta struct {
	Affection int `json:"affection"`
	Trust     int `json:"trust"`
	Fatigue   int `json:"fatigue"`
	Mood      int `json:"mood"`
	Stress    int `json:"stress"`
	Energy    int `json:"energy"`
}

type ChatResponse struct {
	Reply string
	Delta StatusDelta
}

type EventResponse struct {
	Event string      `json:"event"`
	Delta StatusDelta `json:"delta"`
}

type ProactivePayload struct {
	ElapsedTime  string
	CurrentTime  string
	Status       string
	LastMessage  string
	RecentEvents []string
	HotTopics    []string
	CharaPrompt  string
}

type ProactiveResponse struct {
	Send    bool   `json:"send"`
	Message string `json:"message"`
}

// buildPayload はMessagesからGemini APIのペイロードを作る
func buildPayload(messages []Message) map[string]interface{} {
	var systemParts []map[string]interface{}
	var contents []map[string]interface{}

	for _, m := range messages {
		if m.Role == "system" {
			systemParts = append(systemParts, map[string]interface{}{"text": m.Content})
		} else {
			role := m.Role
			if role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]interface{}{
				"role":  role,
				"parts": []map[string]interface{}{{"text": m.Content}},
			})
		}
	}

	payload := map[string]interface{}{"contents": contents}
	if len(systemParts) > 0 {
		payload["system_instruction"] = map[string]interface{}{"parts": systemParts}
	}
	return payload
}

// parseTextResponse はGemini APIのレスポンスからテキストを取り出す
func parseTextResponse(body []byte) (string, error) {
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("パース失敗: %w", err)
	}
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("返答が空")
	}
	return resp.Candidates[0].Content.Parts[0].Text, nil
}

// callGemini は低レイヤのHTTPリクエスト
func callGemini(ctx context.Context, url string, payload interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("JSONマーシャル失敗: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("リクエスト作成失敗: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP通信エラー: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("レスポンス読み込み失敗: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("APIエラー ステータス:%d ボディ:%s", resp.StatusCode, string(body))
	}
	return body, nil
}

// GetChatResponse は簡易会話（Background context）
func GetChatResponse(model string, messages []Message) string {
	res, err := GetChatResponseWithContext(context.Background(), model, messages)
	if err != nil {
		log.Printf("GetChatResponse エラー: %v", err)
		return "（ちょっと調子悪いみたい……）"
	}
	return res
}

// GetChatResponseWithContext はcontext付き会話
func GetChatResponseWithContext(ctx context.Context, model string, messages []Message) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	body, err := callGemini(ctx, url, buildPayload(messages))
	if err != nil {
		return "", err
	}
	return parseTextResponse(body)
}

// GetEmbedding は文章をベクトルに変換する
func GetEmbedding(text string) []float64 {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/gemini-embedding-001:embedContent?key=%s", baseURL, apiKey)

	payload := map[string]interface{}{
		"model":                "models/gemini-embedding-001",
		"outputDimensionality": 1536,
		"content": map[string]interface{}{
			"parts": []map[string]interface{}{{"text": text}},
		},
	}

	body, err := callGemini(context.Background(), url, payload)
	if err != nil {
		log.Printf("GetEmbedding 失敗: %v", err)
		return nil
	}

	var resp struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Printf("Gemini embedding パース失敗: %v", err)
		return nil
	}
	return resp.Embedding.Values
}

// GetChatResponseWithStatus は返答と同時にステータス増減値をJSONで返す
func GetChatResponseWithStatus(ctx context.Context, model string, messages []Message, status string) (*ChatResponse, error) {
	augmented := append([]Message{}, messages...)

	// ステータス情報を最初のsystemに追記
	for i, m := range augmented {
		if m.Role == "system" {
			augmented[i].Content = m.Content + "\n\n" + status
			break
		}
	}

	// JSON形式指示を追加
	augmented = append(augmented, Message{
		Role: "system",
		Content: `返答は必ず以下のJSON形式のみで返してください。

{
  "reply": "返答テキスト",
  "delta": {
    "affection": 増減値（整数）,
    "trust": 増減値,
    "fatigue": 増減値,
    "mood": 増減値,
    "stress": 増減値,
    "energy": 増減値
  }
}`,
	})

	rawResponse, err := GetChatResponseWithContext(ctx, model, augmented)
	if err != nil {
		return nil, err
	}

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "{")
	end := strings.LastIndex(rawResponse, "}")
	if start == -1 || end == -1 {
		return &ChatResponse{Reply: rawResponse}, nil
	}

	var result struct {
		Reply string      `json:"reply"`
		Delta StatusDelta `json:"delta"`
	}
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &result); err != nil {
		return &ChatResponse{Reply: rawResponse}, nil
	}
	return &ChatResponse{Reply: result.Reply, Delta: result.Delta}, nil
}

// GenerateEvent はパートナーの生活イベントを生成する
func GenerateEvent(ctx context.Context, model string, status string, hour int) (*EventResponse, error) {
	timeOfDay := "昼"
	switch {
	case hour >= 5 && hour < 10:
		timeOfDay = "朝"
	case hour >= 10 && hour < 14:
		timeOfDay = "昼"
	case hour >= 14 && hour < 18:
		timeOfDay = "夕方"
	case hour >= 18 && hour < 22:
		timeOfDay = "夜"
	default:
		timeOfDay = "深夜"
	}

	messages := []Message{
		{
			Role: "system",
			Content: `あなたはキャラクターの生活イベントを生成するAIです。
以下のJSONのみを返してください。

{
  "event": "自然な日常の出来事を一文で",
  "delta": {
    "affection": 増減値,
    "trust": 増減値,
    "fatigue": 増減値,
    "mood": 増減値,
    "stress": 増減値,
    "energy": 増減値
  }
}`,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("時間帯：%s\n現在のステータス：%s\n自然な生活イベントを1つ生成してください。", timeOfDay, status),
		},
	}

	rawResponse, err := GetChatResponseWithContext(ctx, model, messages)
	if err != nil {
		return nil, err
	}

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "{")
	end := strings.LastIndex(rawResponse, "}")
	if start == -1 || end == -1 {
		return nil, fmt.Errorf("JSONが見つかりません")
	}

	var result EventResponse
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GenerateProactiveMessage は自発的にメッセージを送るか判断して生成する（Google Search grounding付き）
func GenerateProactiveMessage(ctx context.Context, model string, payload ProactivePayload) (*ProactiveResponse, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("現在時刻: %s\n", payload.CurrentTime))
	sb.WriteString(fmt.Sprintf("経過時間: %s\n", payload.ElapsedTime))
	sb.WriteString(fmt.Sprintf("現在のステータス: %s\n", payload.Status))
	if payload.LastMessage != "" {
		sb.WriteString(fmt.Sprintf("最後の会話: %s\n", payload.LastMessage))
	}
	if len(payload.RecentEvents) > 0 {
		sb.WriteString("最近あったこと:\n")
		for _, e := range payload.RecentEvents {
			sb.WriteString(fmt.Sprintf("  - %s\n", e))
		}
	}
	if len(payload.HotTopics) > 0 {
		sb.WriteString("よく話してる話題:\n")
		for _, t := range payload.HotTopics {
			sb.WriteString(fmt.Sprintf("  - %s\n", t))
		}
	}

	messages := []Message{
		{
			Role: "system",
			Content: payload.CharaPrompt + `

あなたから自発的にメッセージを送るか判断してください。
以下のJSONのみを返してください。

{
  "send": true または false,
  "message": "送る場合のメッセージ（送らない場合は空文字）"
}

送る場合は最近のイベントや話題に絡めて自然に話しかけてください。
web検索で最新情報があれば積極的に使ってください。`,
		},
		{Role: "user", Content: sb.String()},
	}

	reqPayload := buildPayload(messages)
	reqPayload["tools"] = []map[string]interface{}{
		{"google_search": map[string]interface{}{}},
	}

	body, err := callGemini(ctx, url, reqPayload)
	if err != nil {
		return nil, err
	}

	text, err := parseTextResponse(body)
	if err != nil {
		return &ProactiveResponse{Send: false}, nil
	}

	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 {
		return &ProactiveResponse{Send: false}, nil
	}

	var result ProactiveResponse
	if err := json.Unmarshal([]byte(text[start:end+1]), &result); err != nil {
		return &ProactiveResponse{Send: false}, nil
	}
	return &result, nil
}

// UserInfoItem はLLMが抽出したユーザー情報の1件
type UserInfoItem struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Importance float64 `json:"importance"`
}

// ExtractUserInfo は会話からユーザー情報を抽出する
func ExtractUserInfo(ctx context.Context, model string, memories []string) ([]UserInfoItem, error) {
	var sb strings.Builder
	for _, m := range memories {
		sb.WriteString(m + "\n")
	}

	messages := []Message{
		{
			Role: "system",
			Content: `あなたは会話からユーザー（人間側）の情報を抽出するAIです。
以下のJSON配列のみを返してください。抽出できる情報がなければ空配列を返してください。

[
  {
    "key": "情報のキー（例：名前、職業、好きな食べ物、趣味）",
    "value": "情報の値",
    "importance": 0.0から1.0（名前・誕生日=1.0、職業・趣味=0.7、一時的な気分=0.2）
  }
]

ユーザー自身の属性・性質のみ抽出してください。AIの情報や会話の内容は含めないでください。`,
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	rawResponse, err := GetChatResponseWithContext(ctx, model, messages)
	if err != nil {
		return nil, err
	}

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "[")
	end := strings.LastIndex(rawResponse, "]")
	if start == -1 || end == -1 {
		return nil, nil
	}

	var items []UserInfoItem
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// ScheduleItem はLLMが抽出したスケジュール・記念日
type ScheduleItem struct {
	Label  string `json:"label"`
	Date   string `json:"date"` // "YYYY-MM-DD" or "MM-DD"（repeatの場合）
	Repeat bool   `json:"repeat"`
}

// ExtractSchedules は会話からスケジュール・記念日を抽出する
func ExtractSchedules(ctx context.Context, model string, memories []string) ([]ScheduleItem, error) {
	var sb strings.Builder
	for _, m := range memories {
		sb.WriteString(m + "\n")
	}

	messages := []Message{
		{
			Role: "system",
			Content: fmt.Sprintf(`あなたは会話からスケジュールと記念日を抽出するAIです。
今日の日付は %s です。
以下のJSON配列のみを返してください。抽出できる情報がなければ空配列を返してください。

[
  {
    "label": "ラベル（例：誕生日、付き合った日、朝早い、テスト）",
    "date": "YYYY-MM-DD（一時的な予定）またはMM-DD（毎年繰り返す記念日）",
    "repeat": true（毎年繰り返す記念日）またはfalse（一時的な予定）
  }
]

「明日朝早い」→ repeat:false、明日の日付
「誕生日は5月3日」→ repeat:true、MM-DD形式
日付が不明なものは抽出しないでください。`, strings.Split(fmt.Sprintf("%v", fmt.Sprintf("%s", "2006-01-02")), " ")[0]),
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	rawResponse, err := GetChatResponseWithContext(ctx, model, messages)
	if err != nil {
		return nil, err
	}

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "[")
	end := strings.LastIndex(rawResponse, "]")
	if start == -1 || end == -1 {
		return nil, nil
	}

	var items []ScheduleItem
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &items); err != nil {
		return nil, err
	}
	return items, nil
}
