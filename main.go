package main

import (
	"context"
	"fmt"
	"go_app/functions"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type lastConvState struct {
	mu      sync.Mutex
	convID  string
	topicID string
}

func trustGainFor(affection, trust int) int {
	gain := 1 + (affection-trust)/3000
	if gain < 0 {
		gain = 0
	}
	return gain
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println(".envファイルが見つかりません（環境変数から読み込みます）")
	}

	token := os.Getenv("DISCORD_TOKEN")
	dsn := os.Getenv("DATABASE_URL")

	if token == "" {
		log.Fatal("DISCORD_TOKEN が設定されていません")
	}
	if dsn == "" {
		log.Fatal("DATABASE_URL が設定されていません")
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		log.Fatalf("DB接続失敗: %v", err)
	}
	defer db.Close()

	repo := repository.NewMemoryRepository(db)
	repository.RunMigrations(db)

	functions.InitClusters()

	go functions.RunNightlyBatchLoop(repo)
	go functions.RunShiftScheduleLoop(repo)

	activeChars, _ := repo.GetActiveCharacters()
	for _, c := range activeChars {
		go functions.SeedCharaInfoFromPrompt(repo, c)
		go functions.RunEventLoop(repo, c)
	}

	state := &lastConvState{}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Bot作成失敗: %v", err)
	}

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		log.Printf("受信チャンネルID: %s", m.ChannelID)
		charaID := repo.GetCharacterIDByChannel(m.ChannelID)
		log.Printf("キャラID: %s", charaID)

		if charaID == "group" {
			return
		}

		r := repo.WithIDs(m.Author.ID, charaID)

		// 1. 設定取得
		avgTStr := repo.GetSetting("avg_threshold", "0.38")
		maxTStr := repo.GetSetting("max_threshold", "0.50")
		topicTStr := repo.GetSetting("topic_threshold", "0.50")
		mode := repo.GetSetting("chat_mode", "roleplay")
		rulePrompt := repo.GetRulePrompt(mode)
		chara, err := r.GetCharacter(charaID)
		var charaPrompt string
		if err != nil || chara == nil {
			charaPrompt = "あなたは親しみやすい女性です。"
		} else {
			charaPrompt = chara.SystemPrompt
		}
		modelChat := repo.GetSetting("model_chat", "gemini-3-flash-preview")
		modelBatch := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")

		avgT, _ := strconv.ParseFloat(avgTStr, 64)
		maxT, _ := strconv.ParseFloat(maxTStr, 64)
		topicT, _ := strconv.ParseFloat(topicTStr, 64)

		// 2. 入力処理・埋め込み
		userInput := m.Content
		if len(m.Attachments) > 0 && functions.IsImageAttachment(m.Attachments[0].ContentType) {
			desc, err := gemini.DescribeImage(context.Background(), modelBatch, m.Attachments[0].URL)
			if err == nil && desc != "" {
				userInput = userInput + fmt.Sprintf("（添付画像: %s）", desc)
			}
		}
		userEmbedding := gemini.GetEmbedding(userInput)
		convID, isNewConv, _ := r.GetOrCreateConversationID(userEmbedding, avgT, maxT)

		// 3. 話題に紐づける
		topicID, isNewTopic, err := r.GetOrCreateTopic(userEmbedding, topicT)
		if err != nil {
			log.Printf("話題取得失敗: %v", err)
		} else {
			r.LinkConversationToTopic(convID, topicID)
			r.IncrementHeat(topicID)
			r.UpdateTopicEmbedding(topicID, userEmbedding)

			state.mu.Lock()
			prevConvID := state.convID
			prevTopicID := state.topicID
			state.convID = convID
			state.topicID = topicID
			state.mu.Unlock()

			if isNewConv && prevConvID != "" {
				// ユーザー情報・キャラ情報抽出（非同期）
				go func(cid, mbatch, uid, craid string) {
					r := repo.WithIDs(uid, craid)
					memories, err := r.GetRecentMemories(cid, 100)
					if err != nil || len(memories) == 0 {
						return
					}
					var lines []gemini.ExtractInfoMemory
					for _, m := range memories {
						lines = append(lines, gemini.ExtractInfoMemory{Role: m.Role, Content: m.Content})
					}
					result, err := gemini.ExtractUserInfo(context.Background(), mbatch, lines)
					if err != nil || result == nil {
						return
					}

					profileKeys := map[string]bool{"name": true, "age": true, "gender": true, "job": true}
					dedupThreshold, _ := strconv.ParseFloat(repo.GetSetting("chara_info_dedup_threshold", "0.90"), 64)

					for _, item := range result.UserInfo {
						if profileKeys[item.Key] {
							continue
						}
						emb := gemini.GetEmbedding(item.Key + ": " + item.Value)
						existing, _ := r.SearchUserInfoByThreshold(emb, 1, dedupThreshold)
						key := item.Key
						if len(existing) > 0 {
							key = existing[0].Key
						}
						if err := r.UpsertUserInfo(key, item.Value, item.Importance, emb); err != nil {
							log.Printf("ユーザー情報保存失敗: %v", err)
						}
					}

					var name, gender, job string
					var age *int
					for _, item := range result.UserInfo {
						switch item.Key {
						case "name":
							name = item.Value
						case "age":
							var a int
							if _, err := fmt.Sscanf(item.Value, "%d", &a); err == nil {
								age = &a
							}
						case "gender":
							gender = item.Value
						case "job":
							job = item.Value
						}
					}
					if name != "" || age != nil || gender != "" || job != "" {
						if err := r.UpsertUserProfile(name, age, gender, job); err != nil {
							log.Printf("ユーザープロフィール保存失敗: %v", err)
						}
					}

					for _, item := range result.CharaInfo {
						emb := gemini.GetEmbedding(item.Key + ": " + item.Value)
						// 既存キーと照合して近ければキー名を既存に統合
						existing, _ := r.SearchCharaInfo(emb, 1, dedupThreshold, 9999) // trust=9999 でフィルタ無効化
						key := item.Key
						if len(existing) > 0 {
							key = existing[0].Key
						}
						if err := r.UpsertCharaInfo(key, item.Value, item.Importance, emb); err != nil {
							log.Printf("キャラ情報保存失敗: %v", err)

						}
					}
					log.Printf("情報抽出完了: ユーザー%d件 キャラ%d件", len(result.UserInfo), len(result.CharaInfo))
				}(prevConvID, modelBatch, m.Author.ID, charaID)

				// スケジュール抽出（非同期）
				go func(cid, mbatch, uid, craid string) {
					r := repo.WithIDs(uid, craid)
					memories, err := r.GetRecentMemories(cid, 100)
					if err != nil || len(memories) == 0 {
						return
					}
					var lines []string
					for _, m := range memories {
						if m.Role == "user" {
							lines = append(lines, m.Content)
						}
					}
					items, err := gemini.ExtractSchedules(context.Background(), mbatch, lines)
					if err != nil || len(items) == 0 {
						return
					}
					for _, item := range items {
						var date time.Time
						if len(item.Date) == 5 {
							date, err = time.Parse("2006-01-02", fmt.Sprintf("%d-%s", time.Now().Year(), item.Date))
						} else {
							date, err = time.Parse("2006-01-02", item.Date)
						}
						if err != nil {
							continue
						}
						if err := r.UpsertSchedule(item.Label, date, item.Repeat); err != nil {
							log.Printf("スケジュール保存失敗: %v", err)
						}
					}
					log.Printf("スケジュール抽出完了: %d件", len(items))
				}(prevConvID, modelBatch, m.Author.ID, charaID)

				// 会話要約（非同期）
				go func(cid, tid, mbatch, uid, craid string) {
					r := repo.WithIDs(uid, craid)
					if err := r.SummarizeConversation(mbatch, cid, tid); err != nil {
						log.Printf("conversation[%s]要約失敗: %v", cid, err)
					} else {
						log.Printf("conversation[%s]要約完了", cid)
					}
				}(prevConvID, prevTopicID, modelBatch, m.Author.ID, charaID)

				// 日次行動ログ抽出（非同期）— なりきりでは取らない
				if mode != "roleplay" {
					go func(cid, mbatch, uid, craid string) {
						r := repo.WithIDs(uid, craid)
						memories, err := r.GetRecentMemories(cid, 100)
						if err != nil || len(memories) == 0 {
							return
						}
						var lines []gemini.ExtractInfoMemory
						for _, m := range memories {
							lines = append(lines, gemini.ExtractInfoMemory{Role: m.Role, Content: m.Content})
						}
						activities, err := gemini.ExtractDailyActivities(context.Background(), mbatch, lines)
						if err != nil || len(activities) == 0 {
							return
						}
						if err := r.InsertDailyActivities(activities); err != nil {
							log.Printf("日次行動ログ保存失敗: %v", err)
							return
						}
						log.Printf("日次行動ログ保存完了: %d件", len(activities))
					}(prevConvID, modelBatch, m.Author.ID, charaID)
				}

				// 物語ビート要約（なりきり：会話単位で筋を1本記録）
				if mode == "roleplay" {
					go func(cid, mbatch, uid, craid string) {
						r := repo.WithIDs(uid, craid)
						memories, err := r.GetRecentMemories(cid, 100)
						if err != nil || len(memories) == 0 {
							return
						}
						var lines []gemini.ExtractInfoMemory
						for _, m := range memories {
							lines = append(lines, gemini.ExtractInfoMemory{Role: m.Role, Content: m.Content})
						}
						beat, err := gemini.SummarizeSceneBeat(context.Background(), mbatch, lines)
						if err != nil || beat == "" {
							return
						}
						if err := r.AppendSceneBeat(beat); err != nil {
							log.Printf("物語ビート保存失敗: %v", err)
							return
						}
						log.Printf("物語ビート追加: %s", beat)
					}(prevConvID, modelBatch, m.Author.ID, charaID)
				}
			}

			if isNewTopic {
				go func(tid, input, mbatch, uid, craid string) {
					r := repo.WithIDs(uid, craid)
					memories := []repository.Memory{{Role: "user", Content: input}}
					assessment, err := repository.AssessTopic(context.Background(), mbatch, memories)
					if err != nil || assessment == nil {
						return
					}
					r.UpdateTopic(tid, assessment.Keywords, assessment.Summary, assessment.HeatScore*10.0)
					log.Printf("新規話題[%s]即時要約完了: %s %v", tid, assessment.Summary, assessment.Keywords)
				}(topicID, userInput, modelBatch, m.Author.ID, charaID)
			}
		}

		// 4. 短期記憶の取得
		pastMemories, _ := r.GetRecentMemories(convID, 10)
		if len(pastMemories) == 10 {
			allSameConv := true
			for _, m := range pastMemories {
				if m.ConversationID != convID {
					allSameConv = false
					break
				}
			}
			if allSameConv {
				allMemories, err := r.GetAllMemoriesInConversation(convID)
				if err == nil && len(allMemories) > 10 {
					pastMemories = allMemories
				}
			}
		}

		// 5. ステータス・段階プロンプト・内面状態
		status, _ := r.GetPartnerStatus()

		rawStages, _ := r.GetCharacterStages(charaID)
		var stagePrompt string
		if status != nil && len(rawStages) > 0 {
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
			})
		}

		// ── 応答モード判定（propensity）────────────────────────────────────────
		lastUserEmb, _ := r.GetLastUserEmbedding()
		lastAIContent, _ := r.GetLastAIMessageContent()
		propResult := functions.ComputePropensity(functions.PropensityInput{
			UserEmbedding:     userEmbedding,
			LastUserEmbedding: lastUserEmb,
			LastAIContent:     lastAIContent,
			Status:            status,
		})

		if propResult.Mode == functions.ReplySkip {
			emoji := functions.SkipEmoji(propResult, status)
			s.MessageReactionAdd(m.ChannelID, m.ID, emoji)
			debugMode := repo.GetSetting("debug_mode", "false")
			if debugMode == "true" {
				go s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
					"_(skip: %s | score: %.1f)_", propResult.Reason, propResult.Score,
				))
			}
			return
		}
		// ──────────────────────────────────────────────────────────────────────

		// 6. プロフィール・記憶取得
		var profile *gemini.UserProfile
		if prof, err := r.GetUserProfile(); err == nil && prof != nil {
			profile = &gemini.UserProfile{
				Name:   prof.Name,
				Age:    prof.Age,
				Gender: prof.Gender,
				Job:    prof.Job,
			}
		}

		// スケジュール取得
		todaySchedules, _ := r.GetTodaySchedules()
		var scheduleEntries []gemini.ScheduleEntry
		for _, s := range todaySchedules {
			scheduleEntries = append(scheduleEntries, gemini.ScheduleEntry{
				Label: s.Label,
				Date:  s.Date,
			})
		}

		// キャラクターコアプロフィール
		var charaProfile *gemini.CharaProfile
		if prof, err := r.GetCharaProfile(); err == nil && prof != nil {
			charaProfile = &gemini.CharaProfile{
				Name:              prof.Name,
				Age:               prof.Age,
				Gender:            prof.Gender,
				RelationshipStory: prof.RelationshipStory,
			}
		}

		userInfoLimit, _ := strconv.Atoi(repo.GetSetting("user_info_limit", "5"))
		topUserInfos, _ := r.GetTopUserInfo(userInfoLimit)
		searchedInfos, _ := r.SearchUserInfo(userEmbedding, userInfoLimit)
		seenInfoKeys := map[string]bool{}
		var userInfoEntries []gemini.UserInfoEntry
		for _, info := range topUserInfos {
			seenInfoKeys[info.Key] = true
			userInfoEntries = append(userInfoEntries, gemini.UserInfoEntry{Key: info.Key, Value: info.Value})
		}
		for _, info := range searchedInfos {
			if !seenInfoKeys[info.Key] {
				userInfoEntries = append(userInfoEntries, gemini.UserInfoEntry{Key: info.Key, Value: info.Value})
			}
		}

		topTopics, _ := r.GetTopTopics(3)
		searchedTopics, _ := r.SearchTopicsByEmbedding(userEmbedding, topicT)
		topicIDMap := map[string]repository.Topic{}
		for _, t := range topTopics {
			topicIDMap[t.ID] = t
		}
		for _, t := range searchedTopics {
			if _, exists := topicIDMap[t.ID]; !exists {
				topicIDMap[t.ID] = t
			}
		}
		topicSlice := make([]repository.Topic, 0, len(topicIDMap))
		for _, t := range topicIDMap {
			topicSlice = append(topicSlice, t)
		}
		topicSlice = r.FillConvSummaries(topicSlice, userEmbedding)
		var topicEntries []gemini.TopicEntry
		for _, t := range topicSlice {
			topicEntries = append(topicEntries, gemini.TopicEntry{
				Summary:       t.Summary,
				Heat:          t.Heat,
				ConvSummaries: t.ConvSummaries,
			})
		}

		var pastMsgs []gemini.PastMessage
		for _, mem := range pastMemories {
			role := mem.Role
			if role == "proactive" {
				role = "assistant"
			}
			pastMsgs = append(pastMsgs, gemini.PastMessage{
				Role:      role,
				Content:   mem.Content,
				CreatedAt: mem.CreatedAt,
			})
		}

		// chara_info retrieval（trust-gated、top-N + cosine マージ）
		charaInfoLimit, _ := strconv.Atoi(repo.GetSetting("chara_info_limit", "5"))
		charaInfoThreshold, _ := strconv.ParseFloat(repo.GetSetting("chara_info_threshold", "0.45"), 64)
		var charaInfoEntries []gemini.CharaInfoEntry
		if status != nil {
			seenKeys := map[string]bool{}
			topCharaInfos, _ := r.GetTopCharaInfo(charaInfoLimit, status.Trust)
			for _, info := range topCharaInfos {
				seenKeys[info.Key] = true
				charaInfoEntries = append(charaInfoEntries, gemini.CharaInfoEntry{Key: info.Key, Value: info.Value})
			}
			searchedCharaInfos, _ := r.SearchCharaInfo(userEmbedding, charaInfoLimit, charaInfoThreshold, status.Trust)
			for _, info := range searchedCharaInfos {
				if !seenKeys[info.Key] {
					charaInfoEntries = append(charaInfoEntries, gemini.CharaInfoEntry{Key: info.Key, Value: info.Value})
				}
			}
		}

		// 関係イベント取得（検索ヒット → フォールバック上位N件）
		relEventLimit, _ := strconv.Atoi(repo.GetSetting("relationship_events_limit", "3"))
		relEventThreshold, _ := strconv.ParseFloat(repo.GetSetting("relationship_events_threshold", "0.45"), 64)
		var relationshipEvents []string
		searchedRelEvents, _ := r.SearchRelationshipEvents(userEmbedding, relEventLimit, relEventThreshold)
		if len(searchedRelEvents) > 0 {
			for _, e := range searchedRelEvents {
				relationshipEvents = append(relationshipEvents, e.Summary)
			}
		} else {
			topRelEvents, _ := r.GetTopRelationshipEvents(relEventLimit)
			for _, e := range topRelEvents {
				relationshipEvents = append(relationshipEvents, e.Summary)
			}
		}

		var dailyLog []string
		if mode != "roleplay" {
			if logs, err := r.GetTodayDailyLog(); err == nil {
				for _, l := range logs {
					dailyLog = append(dailyLog, l.Event)
				}
			}
		}

		var currentScene string
		var sceneStory string
		if mode == "roleplay" {
			currentScene, _ = r.GetSceneState()
			sceneStory, _ = r.GetSceneStoryText()
		}

		var shiftState string
		if mode != "roleplay" {
			if s, ok, _ := r.ResolveShiftText(time.Now()); ok {
				shiftState = s
			}
		}

		messages := gemini.BuildBaseMessages(gemini.BaseMessagesParams{
			RulePrompt:         rulePrompt,
			CharaPrompt:        charaPrompt,
			StagePrompt:        stagePrompt,
			CurrentScene:       currentScene,
			SceneStory:         sceneStory,
			ShiftState:         shiftState,
			CharaProfile:       charaProfile,
			CharaInfos:         charaInfoEntries,
			Schedules:          scheduleEntries,
			Profile:            profile,
			UserInfos:          userInfoEntries,
			Topics:             topicEntries,
			PastMessages:       pastMsgs,
			RelationshipEvents: relationshipEvents,
			DailyLog:           dailyLog,
		})

		messages = append(messages, gemini.Message{Role: "user", Content: userInput})

		switch propResult.Mode {
		case functions.ReplyShort:
			messages = append(messages, gemini.Message{Role: "system", Content: functions.ShortInstruction()})
		case functions.ReplyDodge:
			messages = append(messages, gemini.Message{Role: "system", Content: functions.DodgeInstruction()})
		}

		innerState, _ := r.GetInnerState()
		var statusText string
		if innerState != nil && innerState.MoodText != "" {
			statusText = "【今の状態・気分】\n" + innerState.MoodText
		} else {
			statusText = stagePrompt
		}

		thinkingBudget, _ := strconv.Atoi(repo.GetSetting("chat_thinking_level", "0"))

		// 7. 生成
		s.ChannelTyping(m.ChannelID)
		reqStart := time.Now()
		var chatResp *gemini.ChatResponse
		if len(m.Attachments) > 0 && functions.IsImageAttachment(m.Attachments[0].ContentType) {
			chatResp, err = gemini.GetChatResponseWithImage(context.Background(), modelChat, messages, m.Attachments[0].URL, statusText, thinkingBudget)
		} else {
			chatResp, err = gemini.GetChatResponseWithStatus(context.Background(), modelChat, messages, statusText, thinkingBudget)
		}
		if err != nil || chatResp == nil {
			log.Printf("GetChatResponseWithStatus失敗: %v", err)
			s.ChannelMessageSend(m.ChannelID, "（ちょっと調子悪いみたい……）")
			return
		}

		aiResponse := chatResp.Reply
		if aiResponse == "" {
			log.Printf("AIの返答が空でした")
			s.ChannelMessageSend(m.ChannelID, "（ちょっと調子悪いみたい……）")
			return
		}

		if propResult.Mode == functions.ReplyShort && chatResp.ReplyType != "skip" {
			chatResp.ReplyType = "short"
			chatResp.Delta = gemini.GetDeltaPreset("short")
		}

		// 8. 返信を先に送る
		debugMode := repo.GetSetting("debug_mode", "false")
		var reply string
		if debugMode == "true" && status != nil {
			reply = fmt.Sprintf("%s\n\n(Conv: %s | Topic: %s | 好感度:%d 気分:%d 活力:%d | %s: %.1f)",
				aiResponse, convID, topicID,
				status.Affection, status.Mood, status.Energy,
				propResult.Reason, propResult.Score,
			)
		} else {
			reply = aiResponse
		}
		sendStart := time.Now()
		s.ChannelMessageSend(m.ChannelID, reply)
		sendElapsed := time.Since(sendStart)

		// 9. 計測ログ
		if debugMode == "true" {
			t := chatResp.Timings
			timingMsg := fmt.Sprintf(
				"```\n[timing]\nGemini API : %dms\n生成合計   : %dms\nDiscord送信: %dms\n受信〜返信 : %dms\n```",
				t.GeminiAPI.Milliseconds(),
				t.Total.Milliseconds(),
				sendElapsed.Milliseconds(),
				time.Since(reqStart).Milliseconds(),
			)
			go s.ChannelMessageSend(m.ChannelID, timingMsg)
		}

		// 10. ステータス変動確定 → goroutine で保存
		finalDelta := repository.StatusDelta(chatResp.Delta)
		finalDelta.Affection += 1
		if chatResp.ReplyType == "normal" && status != nil {
			finalDelta.Trust = trustGainFor(status.Affection, status.Trust)
		}

		go func(input string, inputEmb []float64, response string, cid string, delta repository.StatusDelta) {
			r.SaveMemory(input, inputEmb, "user", cid)
			aiEmb := gemini.GetEmbedding(response)
			r.SaveMemory(response, aiEmb, "assistant", cid)
			if err := r.ApplyStatusDelta(delta); err != nil {
				log.Printf("ステータス更新失敗: %v", err)
			}
		}(userInput, userEmbedding, aiResponse, convID, finalDelta)

		// 現在地トラッキング（なりきり：話題が動いた時だけ抽出して上書き）
		if isNewTopic && mode == "roleplay" {
			go func(uid, craid, mbatch string, ctxMem []repository.Memory, uin, air string) {
				rr := repo.WithIDs(uid, craid)
				prevLoc, _ := rr.GetSceneState()
				if len(ctxMem) > 12 {
					ctxMem = ctxMem[len(ctxMem)-12:]
				}
				var lines []gemini.ExtractInfoMemory
				for _, mm := range ctxMem {
					lines = append(lines, gemini.ExtractInfoMemory{Role: mm.Role, Content: mm.Content})
				}
				lines = append(lines,
					gemini.ExtractInfoMemory{Role: "user", Content: uin},
					gemini.ExtractInfoMemory{Role: "assistant", Content: air},
				)
				loc, err := gemini.ExtractCurrentLocation(context.Background(), mbatch, lines, prevLoc)
				if err != nil || loc == "" {
					return
				}
				if err := rr.UpsertSceneLocation(loc); err != nil {
					log.Printf("現在地保存失敗: %v", err)
					return
				}
				log.Printf("現在地更新: %s", loc)
			}(m.Author.ID, charaID, modelBatch, pastMemories, userInput, aiResponse)
		}
	})

	if err = dg.Open(); err != nil {
		log.Fatalf("接続失敗: %v", err)
	}
	defer dg.Close()

	for _, c := range activeChars {
		go functions.RunProactiveLoop(repo, dg, c)
	}

	fmt.Println("Botが起動しました。CTRL+Cで終了します。")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}
