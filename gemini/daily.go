package gemini

import (
	"context"
	"encoding/json"
	"strings"
)

// ExtractDailyActivities は会話履歴からユーザーの今日の行動・状況を抽出する
func ExtractDailyActivities(ctx context.Context, model string, memories []ExtractInfoMemory) ([]string, error) {
	if len(memories) == 0 {
		return nil, nil
	}

	var sb strings.Builder
	for _, m := range memories {
		if m.Role == "user" {
			sb.WriteString("user: " + m.Content + "\n")
		}
	}

	messages := []Message{
		{
			Role: "system",
			Content: `あなたは会話分析のAIです。
会話履歴からユーザーが「今日」について話した行動・状況・出来事を抽出してください。

【抽出する内容】
- 今日あった出来事（仕事終わった・○○を食べた・○○をしていた等）
- 今日の状態・体調・気分

【抽出しない内容】
- 未来の予定（明日・今度等）
- 一般的な好みや性格（長期的な情報）
- キャラクターに関する情報
- 過去の出来事（昨日以前）

以下のJSONのみを返してください。他の文字は一切出力しないでください。
{"activities": ["仕事が定時で終わった", "スパイスカレーを作っていた"]}

該当する情報がない場合は {"activities": []} を返してください。`,
		},
		{
			Role:    "user",
			Content: sb.String(),
		},
	}

	raw, err := GetChatResponseWithContext(ctx, model, messages, nil)
	if err != nil {
		return nil, err
	}

	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 {
		return nil, nil
	}

	var result struct {
		Activities []string `json:"activities"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return nil, err
	}
	return result.Activities, nil
}
