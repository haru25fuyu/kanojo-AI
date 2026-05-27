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
- "skip": 返信しない（Trust低いのに深い話、しつこい、どうでもいい内容、気分が悪い時など）

{
  "reply": "返答テキスト（skipの場合は空文字）",
  "reply_type": "normal" または "short" または "skip",
  "delta": {
    "affection": 増減値（整数）,
    "trust": 増減値,
    "fatigue": 増減値,
    "mood": 増減値,
    "stress": 増減値,
    "energy": 増減値
  }
}`

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
}

type EventResponse struct {
	Event string      `json:"event"`
	Delta StatusDelta `json:"delta"`
}

type ProactivePayload struct {
	ElapsedTime         string
	CurrentTime         string
	TimeOfDay           string
	ElapsedHours        float64
	TodayProactiveCount int
	TodayConvCount      int
	Status              string
	Affection           int
	Trust               int
	Fatigue             int
	Mood                int
	Stress              int
	Energy              int
	LastMessage         string
	RecentEvents        []string
	HotTopics           []string
	CharaPrompt         string
}

type ProactiveResponse struct {
	Send    bool   `json:"send"`
	Message string `json:"message"`
}

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
	res, err := GetChatResponseWithContext(context.Background(), model, messages)
	if err != nil {
		log.Printf("GetChatResponse エラー: %v", err)
		return "（ちょっと調子悪いみたい……）"
	}
	return res
}

func GetChatResponseWithContext(ctx context.Context, model string, messages []Message) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	body, err := callGemini(ctx, url, buildPayload(messages))
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

func GetChatResponseWithStatus(ctx context.Context, model string, messages []Message, status string) (*ChatResponse, error) {
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

	rawResponse, err := GetChatResponseWithContext(ctx, model, augmented)
	if err != nil {
		log.Printf("GetChatResponseWithContext失敗: %v", err)
		return nil, err
	}
	log.Printf("Gemini生返答: %s", rawResponse[:min(len(rawResponse), 200)])

	rawResponse = strings.TrimSpace(rawResponse)
	start := strings.Index(rawResponse, "{")
	end := strings.LastIndex(rawResponse, "}")
	if start == -1 || end == -1 {
		return &ChatResponse{Reply: rawResponse}, nil
	}

	var result struct {
		Reply     string      `json:"reply"`
		ReplyType string      `json:"reply_type"`
		Delta     StatusDelta `json:"delta"`
	}
	if err := json.Unmarshal([]byte(rawResponse[start:end+1]), &result); err != nil {
		return &ChatResponse{Reply: rawResponse, ReplyType: "normal"}, nil
	}
	if result.ReplyType == "" {
		result.ReplyType = "normal"
	}
	return &ChatResponse{Reply: result.Reply, ReplyType: result.ReplyType, Delta: result.Delta}, nil
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
		`{"event": "キャラクター自身の日常の出来事を一文で", "delta": {"affection": 増減値, "trust": 増減値, "fatigue": 増減値, "mood": 増減値, "stress": 増減値, "energy": 増減値}}`

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

func GenerateProactiveMessage(ctx context.Context, model string, payload ProactivePayload) (*ProactiveResponse, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	// System: 不変のキャラ設定・ルール
	systemPrompt := payload.CharaPrompt +
		"\n\n【send判断】\n" +
		"以下の情報をもとに、このキャラクターとして自発メッセージを送るべきか判断してください。\n" +
		"- 経過時間・今日の自発送信回数・今日の会話数・現在のステータスを総合的に考慮する\n" +
		"- キャラクターの性格として「今送る動機があるか」を自分に問いかける\n" +
		"- 今日すでに自発メッセージを送っていれば、よほどのことがない限りsend: false\n" +
		"- 今日たくさん会話していれば、もう十分としてsend: false\n" +
		"- 疲労が高い・気分が悪い・ストレスが高い場合はsend: false\n" +
		"- 最後の会話が未完了・盛り上がっていた・続きがありそうな場合はsend: false\n" +
		"- 「おやすみ」「また明日」など会話を締めた直後もsend: false\n" +
		"- 「なんとなく話しかけたい」程度ではsend: false、明確な話題や理由があるときだけsend: true\n\n" +
		"【メッセージの作り方】\n" +
		"- 過去に話していた話題をベースに、その続きや近況をユーザーに質問する形にする\n" +
		"- 例：「そういえば〇〇ってどうなった？」「この前〇〇って言ってたけど、最近は？」\n" +
		"- 架空のエピソードや「〜した時みたいに」などの作り話は絶対禁止\n" +
		"- 「一緒に〜しない？」などユーザーを誘う内容は禁止\n" +
		"- キャラクター設定の口調・言葉遣い・世界観を必ず守ること\n" +
		"- 1回のメッセージは2文以内、60文字程度\n\n" +
		"以下のJSONのみを返してください。" + jsonOutputInstruction + "\n\n" +
		`{"send": true または false, "message": "送る場合のメッセージ（送らない場合は空文字）"}`

	// User: 動的なコンテキスト
	var sb strings.Builder
	fmt.Fprintf(&sb, "現在時刻: %s（%s）\n", payload.CurrentTime, payload.TimeOfDay)
	fmt.Fprintf(&sb, "経過時間: %s\n", payload.ElapsedTime)
	fmt.Fprintf(&sb, "ステータス: %s\n", buildStatusText(payload.Affection, payload.Trust, payload.Fatigue, payload.Mood, payload.Stress, payload.Energy))
	fmt.Fprintf(&sb, "今日の自発メッセージ送信回数: %d回\n", payload.TodayProactiveCount)
	fmt.Fprintf(&sb, "今日の会話数: %d回\n", payload.TodayConvCount)
	if payload.LastMessage != "" {
		fmt.Fprintf(&sb, "最後の会話: %s\n", payload.LastMessage)
	}
	if len(payload.RecentEvents) > 0 {
		sb.WriteString("最近あったこと（これをきっかけにしてもよい）:\n")
		for _, e := range payload.RecentEvents {
			fmt.Fprintf(&sb, "  - %s\n", e)
		}
	}
	if len(payload.HotTopics) > 0 {
		sb.WriteString("過去に話していた話題:\n")
		for _, t := range payload.HotTopics {
			fmt.Fprintf(&sb, "  - %s\n", t)
		}
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
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

コアフィールドのキー名は必ず以下に統一すること：
- ユーザーの名前 → key: "name"
- ユーザーの年齢 → key: "age"
- ユーザーの性別 → key: "gender"
- ユーザーの職業・仕事 → key: "job"

{
  "user_info": [
    {
      "key": "情報のキー（例：名前、職業、趣味）",
      "value": "情報の値",
      "importance": 0.0から1.0（名前=1.0、職業・趣味=0.7、一時的な気分=0.2）
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

	rawResponse, err := GetChatResponseWithContext(ctx, model, messages)
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
					{"text": "この画像を10文字程度で一言で説明してください。日本語で。"},
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

func GetChatResponseWithImage(ctx context.Context, model string, messages []Message, imageURL string, statusText string) (*ChatResponse, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	resp, err := http.Get(imageURL)
	if err != nil {
		return nil, fmt.Errorf("画像ダウンロード失敗: %w", err)
	}
	defer resp.Body.Close()

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("画像読み込み失敗: %w", err)
	}

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

	body, err := callGemini(ctx, url, payload)
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
		return &ChatResponse{Reply: text, ReplyType: "normal"}, nil
	}

	var result struct {
		Reply     string      `json:"reply"`
		ReplyType string      `json:"reply_type"`
		Delta     StatusDelta `json:"delta"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &result); err != nil {
		return &ChatResponse{Reply: text, ReplyType: "normal"}, nil
	}
	if result.ReplyType == "" {
		result.ReplyType = "normal"
	}
	return &ChatResponse{Reply: result.Reply, ReplyType: result.ReplyType, Delta: result.Delta}, nil
}