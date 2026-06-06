package gemini

import (
	"fmt"
	"strings"
	"time"
)

type BaseMessagesParams struct {
	RulePrompt   string
	CharaPrompt  string
	StagePrompt  string
	CharaInfos   []CharaInfoEntry
	Profile      *UserProfile
	UserInfos    []UserInfoEntry
	Topics       []TopicEntry
	Events       []EventEntry
	Schedules    []ScheduleEntry
	PastMessages []PastMessage
	CharaProfile *CharaProfile
}

type CharaProfile struct {
	Name   string
	Age    *int
	Gender string
}

type UserProfile struct {
	Name   string
	Age    *int
	Gender string
	Job    string
}

type CharaInfoEntry struct {
	Key   string
	Value string
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

type ScheduleEntry struct {
	Label string
	Date  time.Time
}

type PastMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

func BuildBaseMessages(p BaseMessagesParams) []Message {
	var messages []Message

	messages = append(messages, Message{
		Role:    "system",
		Content: p.RulePrompt,
	})

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

	if p.CharaProfile != nil {
		var parts []string
		if p.CharaProfile.Name != "" {
			parts = append(parts, "名前: "+p.CharaProfile.Name)
		}
		if p.CharaProfile.Age != nil {
			parts = append(parts, fmt.Sprintf("年齢: %d", *p.CharaProfile.Age))
		}
		if p.CharaProfile.Gender != "" {
			parts = append(parts, "性別: "+p.CharaProfile.Gender)
		}
		if len(parts) > 0 {
			messages = append(messages, Message{
				Role:    "system",
				Content: strings.Join(parts, "\n"),
			})
		}
	}

	if len(p.CharaInfos) > 0 {
		var sb strings.Builder
		sb.WriteString("【あなた自身の情報】\n")
		for _, info := range p.CharaInfos {
			fmt.Fprintf(&sb, "- %s: %s\n", info.Key, info.Value)
		}
		messages = append(messages, Message{
			Role:    "system",
			Content: sb.String(),
		})
	}

	if p.Profile != nil {
		var parts []string
		if p.Profile.Name != "" {
			parts = append(parts, "名前: "+p.Profile.Name)
		}
		if p.Profile.Age != nil {
			parts = append(parts, fmt.Sprintf("年齢: %d", *p.Profile.Age))
		}
		if p.Profile.Gender != "" {
			parts = append(parts, "性別: "+p.Profile.Gender)
		}
		if p.Profile.Job != "" {
			parts = append(parts, "職業: "+p.Profile.Job)
		}
		if len(parts) > 0 {
			messages = append(messages, Message{
				Role:    "system",
				Content: "【相手のプロフィール】\n" + strings.Join(parts, "\n"),
			})
		}
	}

	if len(p.UserInfos) > 0 {
		var sb strings.Builder
		sb.WriteString("【相手について知っていること】\n")
		for _, info := range p.UserInfos {
			fmt.Fprintf(&sb, "- %s: %s\n", info.Key, info.Value)
		}
		messages = append(messages, Message{
			Role:    "system",
			Content: sb.String(),
		})
	}

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

	if len(p.Schedules) > 0 {
		var sb strings.Builder
		sb.WriteString("【相手の予定】\n")
		now := time.Now()
		for _, s := range p.Schedules {
			var timing string
			if s.Date.Format("2006-01-02") == now.Format("2006-01-02") {
				timing = "今日"
			} else {
				timing = "明日"
			}
			fmt.Fprintf(&sb, "- %s: %s\n", timing, s.Label)
		}
		messages = append(messages, Message{
			Role:    "system",
			Content: sb.String(),
		})
	}

	now := time.Now()
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
			timeLabel = mem.CreatedAt.Format("1/2")
		}
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
