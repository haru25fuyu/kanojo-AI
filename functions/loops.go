package functions

import (
	"context"
	"fmt"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"math/rand"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
)

func RunNightlyBatchLoop(repo *repository.MemoryRepository) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, now.Location())
		if now.After(next) {
			next = next.Add(24 * time.Hour)
		}
		waitDuration := time.Until(next)
		log.Printf("次のバッチ実行: %s（%s後）", next.Format("01/02 15:04"), waitDuration.Round(time.Minute))
		time.Sleep(waitDuration)

		modelBatch := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")
		repo.RunNightlyBatch(modelBatch)

		activeChars, _ := repo.GetActiveCharacters()

		// 睡眠回復後に全キャラ×全ユーザーのinner_state更新
		for _, c := range activeChars {
			userIDs, _ := repo.GetActiveUserIDsForChara(c.ID)
			for _, uid := range userIDs {
				r := repo.WithIDs(uid, c.ID)
				updateInnerState(r, repo, c)
			}
		}

		// 関係データ更新（全キャラ×全ユーザー）
		for _, c := range activeChars {
			userIDs, _ := repo.GetActiveUserIDsForChara(c.ID)
			for _, uid := range userIDs {
				r := repo.WithIDs(uid, c.ID)
				r.UpdateRelationshipData(context.Background(), modelBatch)
			}
		}
	}
}

func RunEventLoop(repo *repository.MemoryRepository, chara repository.Character) {
	// 起動直後に1回即時生成
	userIDs, _ := repo.GetActiveUserIDsForChara(chara.ID)
	for _, uid := range userIDs {
		r := repo.WithIDs(uid, chara.ID)
		updateInnerState(r, repo, chara)
		log.Printf("inner_state初生成 user=%s: charaID=%v", uid, chara.ID)
	}

	// 起動後1時間半待機
	time.Sleep(90 * time.Minute)

	for {
		userIDs, _ := repo.GetActiveUserIDsForChara(chara.ID)
		for _, uid := range userIDs {
			r := repo.WithIDs(uid, chara.ID)

			hour := time.Now().Hour()
			hourStart, _ := strconv.Atoi(repo.GetSetting("proactive_hour_start", "6"))
			hourEnd, _ := strconv.Atoi(repo.GetSetting("proactive_hour_end", "22"))
			if hour >= hourStart && hour < hourEnd {
				randomDelta := repository.StatusDelta{
					Fatigue: rand.Intn(1000) - 200,
					Mood:    rand.Intn(3000) - 1500,
					Stress:  rand.Intn(1000) - 500,
					Energy:  rand.Intn(3000) - 1500,
				}
				if err := r.ApplyStatusDelta(randomDelta); err != nil {
					log.Printf("乱数デルタ適用失敗 user=%s: %v", uid, err)
				}
				log.Printf("乱数デルタ適用 user=%s: fatigue=%d mood=%d stress=%d energy=%d",
					uid, randomDelta.Fatigue, randomDelta.Mood, randomDelta.Stress, randomDelta.Energy)
				updateInnerState(r, repo, chara)
			}
		}

		time.Sleep(90 * time.Minute)
	}
}

func RunProactiveLoop(repo *repository.MemoryRepository, dg *discordgo.Session, chara repository.Character) {
	startupMinutes, _ := strconv.Atoi(repo.GetSetting("proactive_startup_minutes", "10"))
	time.Sleep(time.Duration(startupMinutes) * time.Minute)

	for {
		checkMinutes, _ := strconv.Atoi(repo.GetSetting("proactive_check_minutes", "30"))
		time.Sleep(time.Duration(checkMinutes) * time.Minute)

		// 最後に話したユーザーを取得してそのステータスを使う
		var lastUserID string
		err := repo.DB().Get(&lastUserID,
			`SELECT user_id FROM conversations WHERE character_id = $1 AND user_id != 'default' ORDER BY updated_at DESC LIMIT 1`,
			chara.ID)
		if err != nil || lastUserID == "" {
			log.Printf("自発メッセージ: ユーザー発言履歴なし、スキップ")
			continue
		}

		r := repo.WithIDs(lastUserID, chara.ID)

		var lastUserTime time.Time
		dbErr := r.DB().Get(&lastUserTime,
			`SELECT created_at FROM memories WHERE character_id = $1 AND user_id = $2 AND role = 'user' ORDER BY id DESC LIMIT 1`,
			chara.ID, lastUserID)
		if dbErr != nil {
			log.Printf("自発メッセージ: ユーザー発言履歴なし、スキップ")
			continue
		}

		now := time.Now()
		elapsed := now.Sub(lastUserTime)
		log.Printf("自発メッセージ: 最終ユーザー発言から%s経過", elapsed.Round(time.Minute))

		minElapsed, _ := strconv.Atoi(repo.GetSetting("proactive_min_elapsed", "60"))
		if elapsed < time.Duration(minElapsed)*time.Minute {
			continue
		}

		var lastRole string
		r.DB().Get(&lastRole,
			`SELECT role FROM memories WHERE character_id = $1 AND user_id = $2 ORDER BY id DESC LIMIT 1`,
			chara.ID, lastUserID)

		forceMinutesCheck, _ := strconv.Atoi(repo.GetSetting("proactive_force_minutes", "4320"))
		if lastRole == "proactive" && elapsed < time.Duration(forceMinutesCheck)*time.Minute {
			log.Printf("自発メッセージ: 前回の自発に返信なしのためスキップ（経過:%s）", elapsed.Round(time.Minute))
			continue
		}

		status, err := r.GetPartnerStatus()
		if err != nil {
			continue
		}

		hotTopics, _ := r.GetTopTopics(3)
		hotTopics = r.FillConvSummaries(hotTopics, nil)
		var topicTexts []string
		for _, t := range hotTopics {
			line := fmt.Sprintf("%s（熱量:%.1f）", t.Summary, t.Heat)
			for _, cs := range t.ConvSummaries {
				line += fmt.Sprintf("\n  - %s", cs)
			}
			topicTexts = append(topicTexts, line)
		}

		var elapsedText string
		switch {
		case elapsed < 2*time.Hour:
			elapsedText = fmt.Sprintf("%.0f分", elapsed.Minutes())
		case elapsed < 24*time.Hour:
			elapsedText = fmt.Sprintf("%.0f時間", elapsed.Hours())
		default:
			elapsedText = fmt.Sprintf("%.0f日", elapsed.Hours()/24)
		}

		var lastMsgLabel string
		if lastUserTime.Format("2006/01/02") == now.Format("2006/01/02") {
			lastMsgLabel = "今日" + lastUserTime.Format("15:04")
		} else if lastUserTime.Format("2006/01/02") == now.AddDate(0, 0, -1).Format("2006/01/02") {
			lastMsgLabel = "昨日" + lastUserTime.Format("15:04")
		} else {
			lastMsgLabel = lastUserTime.Format("01/02 15:04")
		}
		timeConstraint := fmt.Sprintf(
			"（現在 %s / 前回会話: %s / %s経過。この%s間に自然に起きた範囲の出来事だけが語れる事実）\n",
			now.Format("15:04"), lastMsgLabel, elapsedText, elapsedText,
		)

		hour := time.Now().Hour()
		modelChat := repo.GetSetting("model_chat", "gemini-3-flash-preview")
		rulePrompt := r.GetSetting("system_prompt_rule", "日常会話に徹してください。")
		charaPrompt := chara.SystemPrompt
		channelID := chara.ProactiveChannel
		if channelID == "" {
			log.Printf("キャラ[%s]のproactive_channelが未設定", chara.ID)
			continue
		}

		hourStart, _ := strconv.Atoi(repo.GetSetting("proactive_hour_start", "8"))
		hourEnd, _ := strconv.Atoi(repo.GetSetting("proactive_hour_end", "22"))
		if hour < hourStart || hour >= hourEnd {
			log.Printf("自発メッセージ: 時間帯外（%d時）スキップ", hour)
			continue
		}

		var lastConvID string
		r.DB().Get(&lastConvID, `SELECT id FROM conversations WHERE character_id = $1 AND user_id = $2 ORDER BY updated_at DESC LIMIT 1`, chara.ID, lastUserID)
		pastMemories, _ := r.GetRecentMemories(lastConvID, 10)

		rawStages, _ := r.GetCharacterStages(chara.ID)
		var stagePrompt string
		if len(rawStages) > 0 {
			entries := make([]gemini.StageEntry, len(rawStages))
			for i, s := range rawStages {
				e := gemini.StageEntry{
					Parameter: s.Parameter,
					StageFrom: s.StageFrom,
					StageTo:   s.StageTo,
					Prompt:    s.Prompt,
				}
				if s.FilterParam != nil {
					e.FilterParam = *s.FilterParam
					e.FilterFrom = *s.FilterFrom
					e.FilterTo = *s.FilterTo
				}
				entries[i] = e
			}
			stagePrompt = gemini.ResolveStagePrompt(entries, map[string]int{
				"trust":     status.Trust,
				"affection": status.Affection,
				"fatigue":   status.Fatigue,
				"mood":      status.Mood,
				"stress":    status.Stress,
				"energy":    status.Energy,
				"proactive": status.Trust,
			})
		}

		statusText := stagePrompt

		var profile *gemini.UserProfile
		if prof, err := r.GetUserProfile(); err == nil && prof != nil {
			profile = &gemini.UserProfile{
				Name:   prof.Name,
				Age:    prof.Age,
				Gender: prof.Gender,
				Job:    prof.Job,
			}
		}

		var charaProfile *gemini.CharaProfile
		if prof, err := r.GetCharaProfile(); err == nil && prof != nil {
			charaProfile = &gemini.CharaProfile{
				Name:              prof.Name,
				Age:               prof.Age,
				Gender:            prof.Gender,
				RelationshipStory: prof.RelationshipStory,
			}
		}

		userInfoLimit, _ := strconv.Atoi(r.GetSetting("user_info_limit", "5"))
		topUserInfos, _ := r.GetTopUserInfo(userInfoLimit)
		var userInfoEntries []gemini.UserInfoEntry
		for _, info := range topUserInfos {
			userInfoEntries = append(userInfoEntries, gemini.UserInfoEntry{Key: info.Key, Value: info.Value})
		}

		var topicEntries []gemini.TopicEntry
		for _, t := range topicTexts {
			topicEntries = append(topicEntries, gemini.TopicEntry{Summary: t})
		}

		var pastMsgs []gemini.PastMessage
		for _, m := range pastMemories {
			role := m.Role
			if role == "proactive" {
				role = "assistant"
			}
			pastMsgs = append(pastMsgs, gemini.PastMessage{
				Role:      role,
				Content:   m.Content,
				CreatedAt: m.CreatedAt,
			})
		}

		relEventLimit, _ := strconv.Atoi(r.GetSetting("relationship_events_limit", "3"))
		var relationshipEvents []string
		topRelEvents, _ := r.GetTopRelationshipEvents(relEventLimit)
		for _, e := range topRelEvents {
			relationshipEvents = append(relationshipEvents, e.Summary)
		}

		var dailyLog []string
		if logs, err := r.GetTodayDailyLog(); err == nil {
			for _, l := range logs {
				dailyLog = append(dailyLog, l.Event)
			}
		}

		messages := gemini.BuildBaseMessages(gemini.BaseMessagesParams{
			RulePrompt:         rulePrompt,
			CharaPrompt:        charaPrompt,
			StagePrompt:        stagePrompt,
			CharaProfile:       charaProfile,
			Profile:            profile,
			UserInfos:          userInfoEntries,
			Topics:             topicEntries,
			PastMessages:       pastMsgs,
			RelationshipEvents: relationshipEvents,
			DailyLog:           dailyLog,
		})

		forceMinutes, _ := strconv.Atoi(repo.GetSetting("proactive_force_minutes", "180"))
		forceSend := elapsed >= time.Duration(forceMinutes)*time.Minute
		minElapsedDur := time.Duration(minElapsed) * time.Minute

		var proactiveInstruction string
		switch {
		case forceSend:
			proactiveInstruction = timeConstraint + fmt.Sprintf(
				"（%s以上連絡がなかった。必ず話しかけてください）",
				elapsedText,
			)
		case elapsed < 3*minElapsedDur:
			proactiveInstruction = timeConstraint + fmt.Sprintf(
				"（%s連絡がなかった。気になって話しかけたければ、ユーザーの様子を聞いて。特に理由がなければskipでもいい）",
				elapsedText,
			)
		case elapsed < 8*minElapsedDur:
			proactiveInstruction = timeConstraint + fmt.Sprintf(
				"（%s連絡がなかった。ユーザーの様子が気になっている。skipでもいい）",
				elapsedText,
			)
		case elapsed < 24*minElapsedDur:
			proactiveInstruction = timeConstraint + fmt.Sprintf(
				"（%s連絡がなかった。寂しかった気持ちを出しつつユーザーの様子を聞いて。ステータス次第でskipもあり）",
				elapsedText,
			)
		default:
			proactiveInstruction = timeConstraint + fmt.Sprintf(
				"（%s以上連絡がなかった。久しぶりで心配してる、近況を聞いて）",
				elapsedText,
			)
		}

		messages = append(messages, gemini.Message{
			Role:    "user",
			Content: proactiveInstruction,
		})

		thinkingBudget, _ := strconv.Atoi(repo.GetSetting("chat_thinking_budget", "0"))

		chatResp, err := gemini.GetChatResponseWithStatus(context.Background(), modelChat, messages, statusText, thinkingBudget)
		if err != nil || chatResp == nil {
			log.Printf("自発メッセージ生成失敗: %v", err)
			continue
		}

		if chatResp.ReplyType == "skip" && !forceSend {
			log.Printf("自発メッセージ: 見送り（reply_type=skip）（経過:%s）", elapsed.Round(time.Minute))
			continue
		}
		if chatResp.Reply == "" {
			log.Printf("自発メッセージ: 返答が空のためスキップ")
			continue
		}

		dg.ChannelMessageSend(channelID, chatResp.Reply)
		log.Printf("自発メッセージ送信: %s", chatResp.Reply)

		r2 := repo.WithIDs(lastUserID, chara.ID)
		embedding := gemini.GetEmbedding(chatResp.Reply)
		var newConvID string
		err = r2.DB().Get(&newConvID, `INSERT INTO conversations (user_id, character_id) VALUES ($1, $2) RETURNING id`, lastUserID, chara.ID)
		if err == nil {
			r2.SaveMemory(chatResp.Reply, embedding, "proactive", newConvID)
			log.Printf("自発メッセージ履歴保存: conv=%s user=%s", newConvID, lastUserID)
		}
	}
}

func RunScheduleLoop(repo *repository.MemoryRepository, dg *discordgo.Session) {
	for {
		time.Sleep(1 * time.Hour)

		activeChars, err := repo.GetActiveCharacters()
		if err != nil || len(activeChars) == 0 {
			continue
		}

		modelChat := repo.GetSetting("model_chat", "gemini-3-flash-preview")

		for _, ac := range activeChars {
			if ac.ProactiveChannel == "" {
				continue
			}

			// 最後に話したユーザーのステータスを使う
			var lastUserID string
			err := repo.DB().Get(&lastUserID,
				`SELECT user_id FROM conversations WHERE character_id = $1 AND user_id != 'default' ORDER BY updated_at DESC LIMIT 1`,
				ac.ID)
			if err != nil || lastUserID == "" {
				continue
			}

			r := repo.WithIDs(lastUserID, ac.ID)

			schedules, err := r.GetTodaySchedules()
			if err != nil || len(schedules) == 0 {
				continue
			}

			status, _ := r.GetPartnerStatus()
			var statusText string
			if status != nil {
				statusText = fmt.Sprintf("好感度:%d 気分:%d", status.Affection, status.Mood)
			}

			for _, s := range schedules {
				isToday := s.Date.Format("2006-01-02") == time.Now().Format("2006-01-02")
				timing := "明日"
				if isToday {
					timing = "今日"
				}

				messages := []gemini.Message{
					{Role: "system", Content: ac.SystemPrompt},
					{Role: "system", Content: fmt.Sprintf("現在のステータス: %s", statusText)},
					{
						Role: "user",
						Content: fmt.Sprintf(
							"%sは「%s」です。これに関して自然に一言話しかけてください。",
							timing, s.Label,
						),
					},
				}

				reply := gemini.GetChatResponse(modelChat, messages)
				dg.ChannelMessageSend(ac.ProactiveChannel, reply)
				r.MarkNotified(s.ID, s.Repeat)
				log.Printf("スケジュール通知: %s（%s）", s.Label, timing)
			}
		}
	}
}
