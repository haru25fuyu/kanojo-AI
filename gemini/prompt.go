package gemini

import (
	"fmt"
	"strings"
	"time"
)

// BaseMessagesParams はプロンプト組み立てに必要なデータをまとめた構造体
type BaseMessagesParams struct {
	RulePrompt   string
	CharaPrompt  string
	StagePrompt  string
	Profile      *UserProfile
	UserInfos    []UserInfoEntry
	Topics       []TopicEntry
	Events       []EventEntry
	PastMessages []PastMessage
}

type UserProfile struct {
	Name   string
	Age    *int
	Gender string
	Job    string
}

type UserInfoEntry struct {
	Key   string
	Value string
}

type TopicEntry struct {
	Summary       string
	Heat          float64
	ConvSummaries []string
}

type EventEntry struct {
	Event     string
	CreatedAt time.Time
}

type PastMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

// BuildBaseMessages は通常会話・自発メッセージ共通のプロンプトを組み立てる
func BuildBaseMessages(p BaseMessagesParams) []Message {
	var messages []Message

	// 絶対ルール
	messages = append(messages, Message{
		Role:    "system",
		Content: p.RulePrompt,
	})

	// キャラ設定（statusはGetChatResponseWithStatusがここに追記する）
	messages = append(messages, Message{
		Role:    "system",
		Content: p.CharaPrompt,
	})

	if p.StagePrompt != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: p.StagePrompt,
		})
	}

	// 現在時刻
	weekdays := []string{"日", "月", "火", "水", "木", "金", "土"}
	now := time.Now()
	messages = append(messages, Message{
		Role:    "system",
		Content: fmt.Sprintf("【現在時刻】%s（%s）%s", now.Format("2006/01/02"), weekdays[now.Weekday()], now.Format("15:04")),
	})

	// ユーザーコア情報
	if p.Profile != nil {
		var parts []string
		if p.Profile.Name != "" {
			parts = append(parts, "名前: "+p.Profile.Name)
		}
		if p.Profile.Age != nil {
			parts = append(parts, fmt.Sprintf("年齢: %d歳", *p.Profile.Age))
		}
		if p.Profile.Gender != "" {
			parts = append(parts, "性別: "+p.Profile.Gender)
		}
		if p.Profile.Job != "" {
			parts = append(parts, "仕事: "+p.Profile.Job)
		}
		if len(parts) > 0 {
			messages = append(messages, Message{
				Role:    "system",
				Content: "【ユーザーの基本情報】\n- " + strings.Join(parts, "\n- "),
			})
		}
	}

	// ユーザー情報
	if len(p.UserInfos) > 0 {
		var sb strings.Builder
		sb.WriteString("【ユーザーについて知っていること】\n")
		for _, info := range p.UserInfos {
			fmt.Fprintf(&sb, "- %s: %s\n", info.Key, info.Value)
		}
		messages = append(messages, Message{
			Role:    "system",
			Content: sb.String(),
		})
	}

	// 話題の記憶
	if len(p.Topics) > 0 {
		var sb strings.Builder
		sb.WriteString("【あなたが覚えていること】\n")
		for _, t := range p.Topics {
			fmt.Fprintf(&sb, "- %s（熱量: %.1f）\n", t.Summary, t.Heat)
			for _, cs := range t.ConvSummaries {
				fmt.Fprintf(&sb, "  - %s\n", cs)
			}
		}
		messages = append(messages, Message{
			Role:    "system",
			Content: sb.String(),
		})
	}

	// パートナーのイベント
	//if len(p.Events) > 0 {
	//	var sb strings.Builder
	//	sb.WriteString("【最近あったこと】\n")
	//	for _, e := range p.Events {
	//		fmt.Fprintf(&sb, "- %s（%s）\n", e.Event, e.CreatedAt.Format("1/2 15:04"))
	//	}
	//	messages = append(messages, Message{
	//		Role:    "system",
	//		Content: sb.String(),
	//	})
	//}

	// 短期記憶（時刻付き）
	now = time.Now()
	for _, mem := range p.PastMessages {
		diff := now.Sub(mem.CreatedAt)
		var timeLabel string
		switch {
		case diff < 5*time.Minute:
			timeLabel = "さっき"
		case diff < time.Hour:
			timeLabel = fmt.Sprintf("%d分前", int(diff.Minutes()))
		case mem.CreatedAt.Format("2006/01/02") == now.Format("2006/01/02"):
			timeLabel = fmt.Sprintf("今日 %s", mem.CreatedAt.Format("15:04"))
		case diff < 48*time.Hour:
			timeLabel = fmt.Sprintf("昨日 %s", mem.CreatedAt.Format("15:04"))
		case diff < 7*24*time.Hour:
			timeLabel = fmt.Sprintf("%d日前", int(diff.Hours()/24))
		default:
		}
		timeLabel = mem.CreatedAt.Format("1/2")
		role := mem.Role
		if role == "proactive" {
			role = "assistant"
		}
		messages = append(messages, Message{
			Role:    role,
			Content: fmt.Sprintf("(%s) %s", timeLabel, mem.Content),
		})
	}

	return messages
}
