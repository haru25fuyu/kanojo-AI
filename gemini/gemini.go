package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const baseURL = "https://generativelanguage.googleapis.com/v1beta"

// jsonOutputInstruction はJSONのみを返すよう指示する共通プロンプト
const jsonOutputInstruction = "マークダウン記法（```）は使用せず、生のJSONテキストのみを出力すること。"

// chatResponseInstruction は通常返答用のJSON形式指示
var chatResponseInstruction = `返答は必ず以下のJSON形式のみで返してください。` + jsonOutputInstruction + `

reply_typeの判断基準：
- "normal": 通常の返答
- "short": 短い返事だけ（疲れてる・忙しい・素っ気ない時）

{
  "reply": "返答テキスト",
  "reply_type": "normal" または "short"
}`

// deltaPresets は reply_type ごとの固定ステータス変動値
var deltaPresets = map[string]StatusDelta{
	"normal": {Affection: 5, Trust: 3, Fatigue: 5, Mood: 3, Stress: 2, Energy: -3},
	"short":  {Affection: 2, Trust: 1, Fatigue: 8, Mood: -3, Stress: 4, Energy: -5},
	"skip":   {Affection: -3, Trust: -2, Fatigue: 3, Mood: -8, Stress: 6, Energy: -2},
}

func GetDeltaPreset(replyType string) StatusDelta {
	if d, ok := deltaPresets[replyType]; ok {
		return d
	}
	return deltaPresets["normal"]
}

// jsonConfig: JSON抽出用。thinking 0 から始める（精度荒れたら256に上げる）。
var jsonConfig = map[string]interface{}{
	"thinkingConfig":   map[string]interface{}{"thinkingBudget": 0},
	"responseMimeType": "application/json",
}

// buildStatusText はステータスを文字列に変換する共通関数
func buildStatusText(affection, trust, fatigue, mood, stress, energy int) string {
	return fmt.Sprintf("好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d",
		affection, trust, fatigue, mood, stress, energy)
}

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
	Reply     string
	ReplyType string
	Delta     StatusDelta
	Timings   ResponseTimings // 計測結果（呼び出し元がデバッグ表示に使う）
}

// ResponseTimings は各ステップの所要時間を保持する
type ResponseTimings struct {
	GeminiAPI time.Duration // Gemini API 呼び出し時間
	Total     time.Duration // GetChatResponseWithStatus 全体
}

type EventResponse struct {
	Event string `json:"event"`
}

func buildFastConfig(n int) map[string]interface{} {
	tc := map[string]interface{}{}
	switch n {
	case 1:
		tc["thinkingLevel"] = "minimal"
	case 2:
		tc["thinkingLevel"] = "low"
	case 3:
		tc["thinkingLevel"] = "medium"
	case 4:
		tc["thinkingLevel"] = "high"
	default: // 0・不正値 = オフ
		tc["thinkingBudget"] = 0
	}
	return map[string]interface{}{"thinkingConfig": tc}
}

// buildPayload はメッセージとオプションの generationConfig から Gemini API ペイロードを組み立てる。
// genConfig が nil の場合は generationConfig を含めない（既存動作を維持）。
func buildPayload(messages []Message, genConfig map[string]interface{}) map[string]interface{} {
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
	if genConfig != nil {
		payload["generationConfig"] = genConfig
	}
	return payload
}

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

func GetChatResponse(model string, messages []Message) string {
	res, err := GetChatResponseWithContext(context.Background(), model, messages, nil)
	if err != nil {
		log.Printf("GetChatResponse エラー: %v", err)
		return "（ちょっと調子悪いみたい……）"
	}
	return res
}

// GetChatResponseWithContext は genConfig を受け取り buildPayload に渡す。
// nil を渡すと generationConfig なし（既存動作）。
func GetChatResponseWithContext(ctx context.Context, model string, messages []Message, genConfig map[string]interface{}) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	body, err := callGemini(ctx, url, buildPayload(messages, genConfig))
	if err != nil {
		return "", err
	}
	return parseTextResponse(body)
}

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

// GetChatResponseWithStatus は会話返答を生成する。
// thinkingBudget: 0 の fastConfig を使って最速で返す。
// ChatResponse.Timings に各ステップの計測結果を含める。
func GetChatResponseWithStatus(ctx context.Context, model string, messages []Message, status string, thinkingBudget int) (*ChatResponse, error) {
	totalStart := time.Now()

	augmented := append([]Message{}, messages...)

	for i, m := range augmented {
		if m.Role == "system" {
			augmented[i].Content = m.Content + "\n\n" + status
			break
		}
	}

	augmented = append(augmented, Message{
		Role:    "system",
		Content: chatResponseInstruction,
	})

	// ── Gemini API 呼び出し（thinkingオフ） ──
	apiStart := time.Now()
	rawResponse, err := GetChatResponseWithContext(ctx, model, augmented, buildFastConfig(thinkingBudget))
	apiElapsed := time.Since(apiStart)
	if err != nil {
		log.Printf("GetChatResponseWithContext失敗: %v", err)
		return nil, err
	}
	log.Printf("Gemini生返答: %s", rawResponse[:min(len(rawResponse), 200)])

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "{")
	end := strings.LastIndex(rawResponse, "}")
	if start == -1 || end == -1 {
		return &ChatResponse{
			Reply:   rawResponse,
			Timings: ResponseTimings{GeminiAPI: apiElapsed, Total: time.Since(totalStart)},
		}, nil
	}

	var result struct {
		Reply     string `json:"reply"`
		ReplyType string `json:"reply_type"`
	}
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &result); err != nil {
		return &ChatResponse{
			Reply:     rawResponse,
			ReplyType: "normal",
			Delta:     deltaPresets["normal"],
			Timings:   ResponseTimings{GeminiAPI: apiElapsed, Total: time.Since(totalStart)},
		}, nil
	}
	if result.ReplyType == "" {
		result.ReplyType = "normal"
	}
	return &ChatResponse{
		Reply:     result.Reply,
		ReplyType: result.ReplyType,
		Delta:     deltaPresets[result.ReplyType],
		Timings:   ResponseTimings{GeminiAPI: apiElapsed, Total: time.Since(totalStart)},
	}, nil
}

func GenerateEvent(ctx context.Context, model string, status string, hour int, charaPrompt string) (*EventResponse, error) {
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

	eventPrompt := charaPrompt +
		"\n\nあなたはこのキャラクターの生活イベントを生成するAIです。\n" +
		"以下のJSONのみを返してください。\n\n" +
		"【ルール】\n" +
		"- キャラクター自身の単独の出来事のみ生成する\n" +
		"- 「二人で〜」「一緒に〜」「ユーザーと〜」「あなたと〜」は絶対禁止\n" +
		"- キャラクターが一人で経験する自然な日常の出来事にする\n" +
		"- キャラクターの性格・世界観に合った出来事にする\n\n" +
		`{"event": "キャラクター自身の日常の出来事を一文で"}`

	messages := []Message{
		{
			Role:    "system",
			Content: eventPrompt,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("時間帯：%s\n現在のステータス：%s\n自然な生活イベントを1つ生成してください。", timeOfDay, status),
		},
	}

	rawResponse, err := GetChatResponseWithContext(ctx, model, messages, nil)
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

type UserInfoItem struct {
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Importance float64 `json:"importance"`
}

type ExtractInfoResult struct {
	UserInfo  []UserInfoItem `json:"user_info"`
	CharaInfo []UserInfoItem `json:"chara_info"`
}

type ExtractInfoMemory struct {
	Role    string
	Content string
}

func ExtractUserInfo(ctx context.Context, model string, memories []ExtractInfoMemory) (*ExtractInfoResult, error) {
	var sb strings.Builder
	for _, m := range memories {
		role := "ユーザー"
		if m.Role == "assistant" || m.Role == "proactive" {
			role = "キャラクター"
		}
		fmt.Fprintf(&sb, "%s: %s\n", role, m.Content)
	}

	messages := []Message{
		{
			Role: "system",
			Content: `あなたは会話の分析AIです。
「ユーザー:」と「キャラクター:」で始まる会話から情報を抽出し、以下のJSONのみを返してください。

ルール：
- user_info：「ユーザー:」の発言で自分自身について述べた情報、またはキャラクターがユーザーについて言及した情報
- chara_info：「キャラクター:」の発言で自分自身について述べた情報、またはユーザーがキャラクターについて言及した情報
- 呼びかけ（〇〇くん、〇〇ちゃん）から相手の名前を抽出する
- 自分自身への呼びかけは自分のinfoとして抽出する
- 状態の変化（別れた・転職した・やめた・変わった等）は必ず抽出する。変化後の状態をvalueに入れる
  - 例：「彼女と別れた」→ key: "恋愛状況" value: "彼女なし（別れた）"
  - 例：「仕事やめた」→ key: "job" value: "無職（退職）"
  - 例：「引っ越した」→ key: "居住地" value: "引っ越し後の場所（あれば）"
- 過去形・完了形の発言も見逃さない（〜した、〜になった、〜でなくなった）

コアフィールドのキー名は必ず以下に統一すること：
- ユーザーの名前 → key: "name"
- ユーザーの年齢 → key: "age"
- ユーザーの性別 → key: "gender"
- ユーザーの職業・仕事 → key: "job"
- ユーザーの恋愛状況 → key: "恋愛状況"

{
  "user_info": [
    {
      "key": "情報のキー（例：名前、職業、趣味）",
      "value": "情報の値",
      "importance": 0.0から1.0（名前=1.0、職業・趣味=0.7、恋愛状況の変化=0.8、一時的な気分=0.2）
    }
  ],
  "chara_info": [
    {
      "key": "キャラクターの情報キー",
      "value": "情報の値",
      "importance": 0.0から1.0
    }
  ]
}

抽出できない場合は空配列を返してください。`,
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	// JSON抽出なので jsonConfig を使う
	rawResponse, err := GetChatResponseWithContext(ctx, model, messages, jsonConfig)
	if err != nil {
		return nil, err
	}

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "{")
	end := strings.LastIndex(rawResponse, "}")
	if start == -1 || end == -1 {
		return nil, nil
	}

	var result ExtractInfoResult
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

type ScheduleItem struct {
	Label  string `json:"label"`
	Date   string `json:"date"`
	Repeat bool   `json:"repeat"`
}

func ExtractSchedules(ctx context.Context, model string, memories []string) ([]ScheduleItem, error) {
	var sb strings.Builder
	for _, m := range memories {
		fmt.Fprintf(&sb, "%s\n", m)
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
日付が不明なものは抽出しないでください。`, time.Now().Format("2006-01-02")),
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	// JSON配列抽出なので jsonConfig を使う
	rawResponse, err := GetChatResponseWithContext(ctx, model, messages, jsonConfig)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func DescribeImage(ctx context.Context, model string, imageURL string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	resp, err := http.Get(imageURL)
	if err != nil {
		return "", fmt.Errorf("画像ダウンロード失敗: %w", err)
	}
	defer resp.Body.Close()

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("画像読み込み失敗: %w", err)
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/jpeg"
	}

	base64Image := base64.StdEncoding.EncodeToString(imageData)

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "この画像を30文字程度で一言で説明してください。日本語で。"},
					{
						"inline_data": map[string]interface{}{
							"mime_type": mimeType,
							"data":      base64Image,
						},
					},
				},
			},
		},
	}

	body, err := callGemini(ctx, url, payload)
	if err != nil {
		return "", err
	}
	return parseTextResponse(body)
}

// GetChatResponseWithImage は画像付き会話の返答を生成する。
// thinkingBudget: 0 の fastConfig を適用。
// ResponseTimings に画像DL時間・API呼び出し時間を含める。
func GetChatResponseWithImage(ctx context.Context, model string, messages []Message, imageURL string, statusText string, thinkingBudget int) (*ChatResponse, error) {
	totalStart := time.Now()

	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	// 画像DL
	dlStart := time.Now()
	resp, err := http.Get(imageURL)
	if err != nil {
		return nil, fmt.Errorf("画像ダウンロード失敗: %w", err)
	}
	defer resp.Body.Close()

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("画像読み込み失敗: %w", err)
	}
	dlElapsed := time.Since(dlStart)
	log.Printf("[画像] DL: %dms (%d bytes)", dlElapsed.Milliseconds(), len(imageData))

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/jpeg"
	}

	base64Image := base64.StdEncoding.EncodeToString(imageData)

	augmented := append([]Message{}, messages...)
	if statusText != "" {
		augmented = append(augmented, Message{Role: "system", Content: statusText})
	}
	augmented = append(augmented, Message{
		Role:    "system",
		Content: chatResponseInstruction,
	})

	var systemParts []map[string]interface{}
	var contents []map[string]interface{}

	for _, m := range augmented {
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

	if len(contents) > 0 {
		last := contents[len(contents)-1]
		if last["role"] == "user" {
			parts := last["parts"].([]map[string]interface{})
			parts = append(parts, map[string]interface{}{
				"inline_data": map[string]interface{}{
					"mime_type": mimeType,
					"data":      base64Image,
				},
			})
			contents[len(contents)-1]["parts"] = parts
		}
	}

	payload := map[string]interface{}{"contents": contents}
	if len(systemParts) > 0 {
		payload["system_instruction"] = map[string]interface{}{"parts": systemParts}
	}
	// thinking オフ
	payload["generationConfig"] = buildFastConfig(thinkingBudget)

	// API呼び出し
	apiStart := time.Now()
	body, err := callGemini(ctx, url, payload)
	apiElapsed := time.Since(apiStart)
	log.Printf("[画像] API: %dms", apiElapsed.Milliseconds())

	if err != nil {
		return nil, err
	}

	rawText, err := parseTextResponse(body)
	if err != nil {
		return nil, err
	}

	text := strings.TrimSpace(rawText)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 {
		return &ChatResponse{
			Reply:     text,
			ReplyType: "normal",
			Timings:   ResponseTimings{GeminiAPI: apiElapsed, Total: time.Since(totalStart)},
		}, nil
	}

	var result struct {
		Reply     string `json:"reply"`
		ReplyType string `json:"reply_type"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &result); err != nil {
		return &ChatResponse{
			Reply:     text,
			ReplyType: "normal",
			Delta:     deltaPresets["normal"],
			Timings:   ResponseTimings{GeminiAPI: apiElapsed, Total: time.Since(totalStart)},
		}, nil
	}
	if result.ReplyType == "" {
		result.ReplyType = "normal"
	}
	return &ChatResponse{
		Reply:     result.Reply,
		ReplyType: result.ReplyType,
		Delta:     deltaPresets[result.ReplyType],
		Timings:   ResponseTimings{GeminiAPI: apiElapsed, Total: time.Since(totalStart)},
	}, nil
}
