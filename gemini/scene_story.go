package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SummarizeSceneBeat は1会話ぶんの「物語として何が起きたか」を短く要約する。
// topic 要約（AssessTopic）と違い、場面・移動・出来事・関係の変化の筋だけを拾う。
func SummarizeSceneBeat(ctx context.Context, model string, memories []ExtractInfoMemory) (string, error) {
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
	messages := []Message{
		{
			Role: "system",
			Content: `あなたはロールプレイ物語の記録係です。
直前の会話を読み、「物語として何が起きたか」を2〜3文で要約してください。場所の移動・起きた出来事・2人の関係の変化など、後で物語を振り返るのに必要な筋だけを書きます。セリフの逐語や些末なやり取りは省くこと。
以下のJSONのみを返してください。他の文字は一切出力しないでください。
{"beat": "森を抜けて城下町に着き、宿で今後の計画を話した。"}`,
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
		Beat string `json:"beat"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return "", err
	}
	return result.Beat, nil
}

// FoldSceneStory は既存のあらすじとその日のビート群を、1本の連続した物語へ畳み込む（夜バッチ用）。
// 直近は具体的に、古い部分は圧縮する（一晩寝て覚えている程度の粒度）。
func FoldSceneStory(ctx context.Context, model string, prevSummary string, beats []string) (string, error) {
	if prevSummary == "" && len(beats) == 0 {
		return "", nil
	}
	var sb strings.Builder
	if prevSummary != "" {
		sb.WriteString("これまでのあらすじ:\n")
		sb.WriteString(prevSummary)
		sb.WriteString("\n\n")
	}
	if len(beats) > 0 {
		sb.WriteString("今日起きたこと:\n")
		for _, b := range beats {
			fmt.Fprintf(&sb, "- %s\n", b)
		}
	}
	messages := []Message{
		{
			Role: "system",
			Content: `あなたはロールプレイ物語の記録係です。
「これまでのあらすじ」と「今日起きたこと」を統合し、1本の連続した物語のあらすじに畳み込んでください。
- 直近の展開は具体的に、古い部分は要点だけに圧縮する（人が一晩寝て覚えている程度の粒度）。
- 今いる場面・状況・2人の関係が分かるように締める。
- 筋を保ったまま、全体が長くなりすぎないようにまとめる。
あらすじの本文だけを出力してください（前置き・JSON・箇条書きは不要）。`,
		},
		{Role: "user", Content: sb.String()},
	}
	raw, err := GetChatResponseWithContext(ctx, model, messages, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}
