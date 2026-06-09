package gemini

import (
	"context"
	"fmt"
	"strings"
)

// JudgeConversationWeight は会話要約が感情的に重い出来事かを二値判定する。
// 通常時セントロイドの外れ値として検出された候補に対して呼ぶ。
func JudgeConversationWeight(ctx context.Context, model string, summary string) (bool, error) {
	messages := []Message{
		{
			Role: "system",
			Content: `あなたは会話分析の専門家です。
与えられた会話要約が「感情的に重要な出来事」かどうかを判定してください。

【重要な出来事の基準】
- 普段は話さないような個人的・感情的な話題に踏み込んだ
- 関係の転換点になりえる出来事があった
- 後から「あのとき〇〇を話した」と自然に参照されうる内容がある

【重要でない例】
- 日常的な近況報告
- 雑談・軽い話題
- 短いやりとりで終わった会話

「yes」または「no」のみ返してください。他の文字は一切出力しないでください。`,
		},
		{
			Role:    "user",
			Content: summary,
		},
	}

	raw, err := GetChatResponseWithContext(ctx, model, messages, nil)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(strings.ToLower(raw)) == "yes", nil
}

// GenerateRelationshipStory はイベント一覧とステータスからストーリーラインを生成する。
// 生成結果は chara_profile.relationship_story に保存して常時注入する。
func GenerateRelationshipStory(ctx context.Context, model string, eventSummaries []string, affection, trust int) (string, error) {
	var sb strings.Builder
	for i, s := range eventSummaries {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, s)
	}

	messages := []Message{
		{
			Role: "system",
			Content: `あなたはAIキャラクターの記憶を管理するシステムです。
与えられた重要な出来事の一覧と関係値をもとに、「この関係がどういう経緯で今に至るか」を2〜4文でまとめてください。

【ルール】
- キャラクター視点ではなく、関係の状況として客観的に書く
- 「〇〇という出来事があった。その後〇〇になった」という経緯が伝わるように書く
- 余計な感情表現・修飾は不要。事実ベースで簡潔に
- 「アシスタント」「AI」という表現は使わない
- 日本語で出力する
- 2〜4文に収める`,
		},
		{
			Role: "user",
			Content: fmt.Sprintf(
				"好感度: %d / 信頼度: %d\n\n重要な出来事:\n%s",
				affection, trust, sb.String(),
			),
		},
	}

	return GetChatResponseWithContext(ctx, model, messages, nil)
}
