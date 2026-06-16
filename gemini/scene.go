package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractCurrentLocation はロールプレイの直近会話から「今いる場所・状況」を1句で抜く。
// prevLocation を渡すことで、場面が動いていなければそのまま返させる（上書き前提なので no-op になる）。
func ExtractCurrentLocation(ctx context.Context, model string, memories []ExtractInfoMemory, prevLocation string) (string, error) {
	if len(memories) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for _, m := range memories {
		role := "ユーザー"
		if m.Role == "assistant" || m.Role == "proactive" {
			role = "キャラクター"
		}
		fmt.Fprintf(&sb, "%s: %s\n", role, m.Content)
	}

	prev := prevLocation
	if prev == "" {
		prev = "（未設定）"
	}

	messages := []Message{
		{
			Role: "system",
			Content: `あなたはロールプレイの場面を追跡するAIです。
直前までの会話を読み、「今この2人がいる場所・状況」を短い一句で答えてください（例：「洞窟の奥」「城へ向かう道中」「酒場のテーブル席」）。

直前の場所: ` + prev + `

- 場面が動いていなければ、直前の場所をそのまま返す。
- 移動の途中なら「Aへ向かう道中」のように書く。
- 場所が判断できない場合も、直前の場所をそのまま返す（勝手に変えない）。

以下のJSONのみを返してください。他の文字は一切出力しないでください。
{"location": "洞窟の奥"}`,
		},
		{Role: "user", Content: sb.String()},
	}

	raw, err := GetChatResponseWithContext(ctx, model, messages, nil)
	if err != nil {
		return "", err
	}

	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 {
		return "", nil
	}

	var result struct {
		Location string `json:"location"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return "", err
	}
	return result.Location, nil
}
