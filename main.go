package main

import (
	"context"
	"fmt"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
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

	go runNightlyBatchLoop(repo)

	activeChars, _ := repo.GetActiveCharacters()

	for _, c := range activeChars {
		go runEventLoop(repo, c)
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

		repo.UserID = m.Author.ID
		repo.CharacterID = charaID

		// 1. 設定取得
		avgTStr := repo.GetSetting("avg_threshold", "0.38")
		maxTStr := repo.GetSetting("max_threshold", "0.50")
		topicTStr := repo.GetSetting("topic_threshold", "0.50")
		rulePrompt := repo.GetSetting("system_prompt_rule", "日常会話に徹してください。")
		chara, err := repo.GetCharacter(repo.CharacterID)
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

		// 2. 会話ID取得
		userInput := m.Content
		if len(m.Attachments) > 0 && isImageAttachment(m.Attachments[0].ContentType) {
			desc, err := gemini.DescribeImage(context.Background(), modelBatch, m.Attachments[0].URL)
			if err == nil && desc != "" {
				userInput = userInput + fmt.Sprintf("（添付画像: %s）", desc)
			}
		}
		userEmbedding := gemini.GetEmbedding(userInput)
		convID, isNewConv, _ := repo.GetOrCreateConversationID(userEmbedding, avgT, maxT)

		// 3. 話題に紐づける
		topicID, isNewTopic, err := repo.GetOrCreateTopic(userEmbedding, topicT)
		if err != nil {
			log.Printf("話題取得失敗: %v", err)
		} else {
			repo.LinkConversationToTopic(convID, topicID)
			repo.IncrementHeat(topicID)
			repo.UpdateTopicEmbedding(topicID, userEmbedding)

			state.mu.Lock()
			prevConvID := state.convID
			prevTopicID := state.topicID
			state.convID = convID
			state.topicID = topicID
			state.mu.Unlock()

			if isNewConv && prevConvID != "" {
				// ユーザー情報抽出（非同期）
				go func(cid, mbatch, uid, craid string) {
					r := repo.WithIDs(uid, craid)
					memories, err := r.GetRecentMemories(cid, 100)
					if err != nil || len(memories) == 0 {
						return
					}
					var lines []gemini.ExtractInfoMemory
					for _, m := range memories {
						lines = append(lines, gemini.ExtractInfoMemory{
							Role:    m.Role,
							Content: m.Content,
						})
					}
					result, err := gemini.ExtractUserInfo(context.Background(), mbatch, lines)
					if err != nil || result == nil {
						return
					}
					for _, item := range result.UserInfo {
						emb := gemini.GetEmbedding(item.Key + ": " + item.Value)
						if err := r.UpsertUserInfo(item.Key, item.Value, item.Importance, emb); err != nil {
							log.Printf("ユーザー情報保存失敗: %v", err)
						}
					}

					// コアフィールドをuser_profileに反映
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
						if err := r.UpsertCharaInfo(item.Key, item.Value, item.Importance, emb); err != nil {
							log.Printf("キャラ情報保存失敗: %v", err)
						}
					}
					log.Printf("情報抽出完了: ユーザー%d件 キャラ%d件", len(result.UserInfo), len(result.CharaInfo))
				}(prevConvID, modelBatch, repo.UserID, repo.CharacterID)

				// スケジュール抽出（非同期）
				go func(cid, mbatch, uid string) {
					r := repo.WithIDs(uid, repo.CharacterID)
					memories, err := r.GetRecentMemories(cid, 100)
					if err != nil || len(memories) == 0 {
						return
					}
					var lines []string
					for _, m := range memories {
						lines = append(lines, m.Role+": "+m.Content)
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
				}(prevConvID, modelBatch, repo.UserID)

				// 会話要約（非同期）
				go func(cid, tid, mbatch, uid, craid string) {
					r := repo.WithIDs(uid, craid)
					if err := r.SummarizeConversation(mbatch, cid, tid); err != nil {
						log.Printf("conversation[%s]要約失敗: %v", cid, err)
					} else {
						log.Printf("conversation[%s]要約完了", cid)
					}
				}(prevConvID, prevTopicID, modelBatch, repo.UserID, repo.CharacterID)
			}

			// 新規話題のときだけ即時要約
			if isNewTopic {
				go func(tid, input, mbatch, craid string) {
					r := &repository.MemoryRepository{}
					*r = *repo
					r.CharacterID = craid
					memories := []repository.Memory{{Role: "user", Content: input}}
					assessment, err := repository.AssessTopic(context.Background(), mbatch, memories)
					if err != nil || assessment == nil {
						return
					}
					r.UpdateTopic(tid, assessment.Keywords, assessment.Summary, assessment.HeatScore*10.0)
					log.Printf("新規話題[%s]即時要約完了: %s %v", tid, assessment.Summary, assessment.Keywords)
				}(topicID, userInput, modelBatch, repo.CharacterID)
			}
		}

		// 4. 短期記憶の取得
		pastMemories, _ := repo.GetRecentMemories(convID, 10)
		if len(pastMemories) == 10 {
			allSameConv := true
			for _, m := range pastMemories {
				if m.ConversationID != convID {
					allSameConv = false
					break
				}
			}
			if allSameConv {
				allMemories, err := repo.GetAllMemoriesInConversation(convID)
				if err == nil && len(allMemories) > 10 {
					pastMemories = allMemories
				}
			}
		}

		// 5. プロンプトの組み立て

		// プロフィール取得
		var gProfile *gemini.UserProfile
		if prof, err := repo.GetUserProfile(); err == nil && prof != nil {
			var age *int
			if prof.Age != nil {
				age = prof.Age
			}
			gProfile = &gemini.UserProfile{
				Name: prof.Name, Age: age, Gender: prof.Gender, Job: prof.Job,
			}
		}

		// ユーザー情報収集
		userInfoLimit, _ := strconv.Atoi(repo.GetSetting("user_info_limit", "5"))
		topUserInfos, _ := repo.GetTopUserInfo(userInfoLimit)
		searchedInfos, _ := repo.SearchUserInfo(userEmbedding, userInfoLimit)
		userInfoMap := map[string]bool{}
		var userInfoEntries []gemini.UserInfoEntry
		for _, info := range topUserInfos {
			userInfoMap[info.Key] = true
			userInfoEntries = append(userInfoEntries, gemini.UserInfoEntry{Key: info.Key, Value: info.Value})
		}
		for _, info := range searchedInfos {
			if !userInfoMap[info.Key] {
				userInfoEntries = append(userInfoEntries, gemini.UserInfoEntry{Key: info.Key, Value: info.Value})
			}
		}

		// 話題収集
		topTopics, _ := repo.GetTopTopics(3)
		searchedTopics, _ := repo.SearchTopicsByEmbedding(userEmbedding, topicT)
		topicMap := map[string]bool{}
		var topicEntries []gemini.TopicEntry
		for _, t := range topTopics {
			topicMap[t.ID] = true
			topicEntries = append(topicEntries, gemini.TopicEntry{Summary: t.Summary, Heat: t.Heat, ConvSummaries: t.ConvSummaries})
		}
		for _, t := range searchedTopics {
			if !topicMap[t.ID] {
				topicEntries = append(topicEntries, gemini.TopicEntry{Summary: t.Summary, Heat: t.Heat, ConvSummaries: t.ConvSummaries})
			}
		}
		// FillConvSummariesを適用
		for i, t := range topTopics {
			if topicMap[t.ID] {
				_ = i
			}
		}
		topicSlice := make([]repository.Topic, 0)
		for _, t := range topTopics {
			topicSlice = append(topicSlice, t)
		}
		for _, t := range searchedTopics {
			if !topicMap[t.ID] {
				topicSlice = append(topicSlice, t)
			}
		}
		topicSlice = repo.FillConvSummaries(topicSlice)
		topicEntries = nil
		for _, t := range topicSlice {
			topicEntries = append(topicEntries, gemini.TopicEntry{Summary: t.Summary, Heat: t.Heat, ConvSummaries: t.ConvSummaries})
		}

		// イベント収集
		recentEvents, _ := repo.GetRecentEvents(3)
		relatedEvents, _ := repo.SearchEvents(userEmbedding, 3)
		eventMap := map[int64]bool{}
		var eventEntries []gemini.EventEntry
		for _, e := range recentEvents {
			eventMap[e.ID] = true
			eventEntries = append(eventEntries, gemini.EventEntry{Event: e.Event, CreatedAt: e.CreatedAt})
		}
		for _, e := range relatedEvents {
			if !eventMap[e.ID] {
				eventEntries = append(eventEntries, gemini.EventEntry{Event: e.Event, CreatedAt: e.CreatedAt})
			}
		}

		// 短期記憶変換
		var pastMsgs []gemini.PastMessage
		for _, mem := range pastMemories {
			pastMsgs = append(pastMsgs, gemini.PastMessage{
				Role: mem.Role, Content: mem.Content, CreatedAt: mem.CreatedAt,
			})
		}

		messages := gemini.BuildBaseMessages(gemini.BaseMessagesParams{
			RulePrompt:   rulePrompt,
			CharaPrompt:  charaPrompt,
			Profile:      gProfile,
			UserInfos:    userInfoEntries,
			Topics:       topicEntries,
			Events:       eventEntries,
			PastMessages: pastMsgs,
		})

		// 今回の発言
		messages = append(messages, gemini.Message{
			Role:    "user",
			Content: userInput,
		})

		// 6. ステータス取得
		status, _ := repo.GetPartnerStatus()
		var statusText string
		if status != nil {
			statusText = fmt.Sprintf(
				"【現在のパートナーステータス】\n好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d\nこのステータスに基づいて返答してください。",
				status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy,
			)
		}

		// 7. 生成
		var chatResp *gemini.ChatResponse
		if len(m.Attachments) > 0 && isImageAttachment(m.Attachments[0].ContentType) {
			chatResp, err = gemini.GetChatResponseWithImage(context.Background(), modelChat, messages, m.Attachments[0].URL, statusText)
		} else {
			chatResp, err = gemini.GetChatResponseWithStatus(context.Background(), modelChat, messages, statusText)
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

		// 8. 返信を先に送る（保存は後でgoroutineに任せる）
		debugMode := repo.GetSetting("debug_mode", "false")
		var reply string
		if debugMode == "true" && status != nil {
			reply = fmt.Sprintf("%s\n\n(Conv: %s | Topic: %s | 好感度:%d 気分:%d 活力:%d)",
				aiResponse, convID, topicID, status.Affection, status.Mood, status.Energy)
		} else {
			reply = aiResponse
		}
		sendStart := time.Now()
		s.ChannelMessageSend(m.ChannelID, reply)
		sendElapsed := time.Since(sendStart)

		// 9. タイミングログ（debug_mode時）
		if debugMode == "true" {
			t := chatResp.Timings
			timingMsg := fmt.Sprintf(
				"```\n[timing]\nGemini API : %dms\n生成合計   : %dms\nDiscord送信: %dms\n```",
				t.GeminiAPI.Milliseconds(),
				t.Total.Milliseconds(),
				sendElapsed.Milliseconds(),
			)
			go s.ChannelMessageSend(m.ChannelID, timingMsg)
		}

		// 10. 保存・ステータス更新をgoroutineに逃がす
		go func(
			input string,
			inputEmb []float64,
			response string,
			cid string,
			delta repository.StatusDelta,
		) {
			repo.SaveMemory(input, inputEmb, "user", cid)
			aiEmb := gemini.GetEmbedding(response)
			repo.SaveMemory(response, aiEmb, "assistant", cid)
			delta.Affection += 1
			if err := repo.ApplyStatusDelta(delta); err != nil {
				log.Printf("ステータス更新失敗: %v", err)
			}
		}(userInput, userEmbedding, aiResponse, convID, repository.StatusDelta(chatResp.Delta))
	})

	if err = dg.Open(); err != nil {
		log.Fatalf("接続失敗: %v", err)
	}
	defer dg.Close()

	for _, c := range activeChars {
		go runProactiveLoop(repo, dg, c)
	}
	go runScheduleLoop(repo, dg)

	fmt.Println("Botが起動しました。CTRL+Cで終了します。")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func runNightlyBatchLoop(repo *repository.MemoryRepository) {
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
	}
}

func runEventLoop(repo *repository.MemoryRepository, chara repository.Character) {
	r := repo.WithIDs("default", chara.ID)
	for {
		time.Sleep(90 * time.Minute)

		status, err := r.GetPartnerStatus()
		if err != nil {
			log.Printf("イベント生成: ステータス取得失敗: %v", err)
			continue
		}

		modelBatch := r.GetSetting("model_batch", "gemini-3.1-flash-lite")
		statusText := fmt.Sprintf("好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d",
			status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy)

		charaData, _ := r.GetCharacter(chara.ID)
		var charaPrompt string
		if charaData != nil {
			charaPrompt = charaData.SystemPrompt
		}
		result, err := gemini.GenerateEvent(context.Background(), modelBatch, statusText, time.Now().Hour(), charaPrompt)
		if err != nil || result == nil {
			log.Printf("イベント生成失敗: %v", err)
			continue
		}

		embedding := gemini.GetEmbedding(result.Event)
		if err := r.SaveEvent(result.Event, embedding); err != nil {
			log.Printf("イベント保存失敗: %v", err)
			continue
		}

		// delta は乱数で毎回適用（好感度・信頼度は除外）
		randomDelta := repository.StatusDelta{
			Fatigue: rand.Intn(2000) - 1000,
			Mood:    rand.Intn(2000) - 1000,
			Stress:  rand.Intn(1000) - 500,
			Energy:  rand.Intn(1000) - 500,
		}
		if err := r.ApplyStatusDelta(randomDelta); err != nil {
			log.Printf("乱数デルタ適用失敗: %v", err)
		}

		log.Printf("イベント発生: %s", result.Event)
	}
}

func runProactiveLoop(repo *repository.MemoryRepository, dg *discordgo.Session, chara repository.Character) {
	r := repo.WithIDs("default", chara.ID)
	startupMinutes, _ := strconv.Atoi(repo.GetSetting("proactive_startup_minutes", "10"))
	time.Sleep(time.Duration(startupMinutes) * time.Minute)

	for {
		checkMinutes, _ := strconv.Atoi(repo.GetSetting("proactive_check_minutes", "30"))
		time.Sleep(time.Duration(checkMinutes) * time.Minute)

		var lastTime time.Time
		dbErr := r.DB().Get(&lastTime,
			`SELECT created_at FROM memories WHERE character_id = $1 ORDER BY id DESC LIMIT 1`,
			chara.ID)
		if dbErr != nil {
			log.Printf("自発メッセージ: 履歴なし、スキップ")
			continue
		}

		elapsed := time.Since(lastTime)
		log.Printf("自発メッセージ: 最終メッセージから%s経過", elapsed.Round(time.Minute))

		minElapsed, _ := strconv.Atoi(repo.GetSetting("proactive_min_elapsed", "60"))
		if elapsed < time.Duration(minElapsed)*time.Minute {
			continue
		}

		var lastRole string
		r.DB().Get(&lastRole,
			`SELECT role FROM memories WHERE character_id = $1 ORDER BY id DESC LIMIT 1`,
			chara.ID)

		if lastRole == "user" && elapsed < 30*time.Minute {
			log.Printf("自発メッセージ: 会話中のためスキップ")
			continue
		}

		forceMinutesCheck, _ := strconv.Atoi(repo.GetSetting("proactive_force_minutes", "4320"))
		if lastRole == "proactive" && elapsed < time.Duration(forceMinutesCheck)*time.Minute {
			log.Printf("自発メッセージ: 前回も自発メッセージのためスキップ（経過:%s）", elapsed.Round(time.Minute))
			continue
		}

		// 必要な情報を集める
		status, err := r.GetPartnerStatus()
		if err != nil {
			continue
		}
		statusText := fmt.Sprintf("好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d",
			status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy)

		lastMsg, _ := r.GetLastMemory()

		recentEvents, _ := r.GetRecentEvents(3)
		var eventTexts []string
		for _, e := range recentEvents {
			eventTexts = append(eventTexts, e.Event)
		}

		hotTopics, _ := r.GetTopTopics(3)
		var topicTexts []string
		for _, t := range hotTopics {
			topicTexts = append(topicTexts, fmt.Sprintf("%s（熱量:%.1f）", t.Summary, t.Heat))
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

		hour := time.Now().Hour()
		var timeOfDay string
		switch {
		case hour >= 6 && hour < 10:
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

		modelChat := r.GetSetting("model_chat", "gemini-3-flash-preview")
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

		todayProactiveCount := r.GetTodayProactiveCount()
		todayConvCount := r.GetTodayConvCount()

		result, err := gemini.GenerateProactiveMessage(context.Background(), modelChat, gemini.ProactivePayload{
			ElapsedTime:         elapsedText,
			ElapsedHours:        elapsed.Hours(),
			CurrentTime:         time.Now().Format("2006/01/02 15:04"),
			TimeOfDay:           timeOfDay,
			TodayProactiveCount: todayProactiveCount,
			TodayConvCount:      todayConvCount,
			Status:              statusText,
			Affection:           status.Affection,
			Trust:               status.Trust,
			Fatigue:             status.Fatigue,
			Mood:                status.Mood,
			Stress:              status.Stress,
			Energy:              status.Energy,
			LastMessage:         lastMsg,
			RecentEvents:        eventTexts,
			HotTopics:           topicTexts,
			CharaPrompt:         charaPrompt,
		})
		forceMinutes, _ := strconv.Atoi(repo.GetSetting("proactive_force_minutes", "180"))
		forceSend := elapsed >= time.Duration(forceMinutes)*time.Minute

		if !forceSend && (err != nil || result == nil || !result.Send) {
			log.Printf("自発メッセージ: 見送り（経過:%s）", elapsed.Round(time.Minute))
			continue
		}

		if forceSend && (err != nil || result == nil || !result.Send) {
			log.Printf("自発メッセージ: 強制送信（経過:%s）", elapsed.Round(time.Minute))
			forcePrompt := chara.SystemPrompt + "\n\n【重要】長時間連絡が取れていません。必ずsend: trueにして話しかけてください。"
			forceResult, ferr := gemini.GenerateProactiveMessage(context.Background(), modelChat, gemini.ProactivePayload{
				ElapsedTime:         elapsedText,
				ElapsedHours:        elapsed.Hours(),
				CurrentTime:         time.Now().Format("2006/01/02 15:04"),
				TimeOfDay:           timeOfDay,
				TodayProactiveCount: todayProactiveCount,
				TodayConvCount:      todayConvCount,
				Status:              statusText,
				Affection:           status.Affection,
				Trust:               status.Trust,
				Fatigue:             status.Fatigue,
				Mood:                status.Mood,
				Stress:              status.Stress,
				Energy:              status.Energy,
				LastMessage:         "",
				RecentEvents:        eventTexts,
				HotTopics:           topicTexts,
				CharaPrompt:         forcePrompt,
			})
			if ferr != nil || forceResult == nil {
				continue
			}
			result = forceResult
			result.Send = true
		}

		dg.ChannelMessageSend(channelID, result.Message)
		log.Printf("自発メッセージ送信: %s", result.Message)

		// 自発メッセージを履歴に保存
		var lastUserID string
		r.DB().Get(&lastUserID, `SELECT user_id FROM conversations WHERE character_id = $1 ORDER BY updated_at DESC LIMIT 1`, chara.ID)
		if lastUserID != "" {
			r2 := repo.WithIDs(lastUserID, chara.ID)
			embedding := gemini.GetEmbedding(result.Message)
			var newConvID string
			err := r2.DB().Get(&newConvID, `INSERT INTO conversations (user_id, character_id) VALUES ($1, $2) RETURNING id`, lastUserID, chara.ID)
			if err == nil {
				r2.SaveMemory(result.Message, embedding, "proactive", newConvID)
				log.Printf("自発メッセージ履歴保存: conv=%s user=%s", newConvID, lastUserID)
			}
		}
	}
}

func runScheduleLoop(repo *repository.MemoryRepository, dg *discordgo.Session) {
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
			r := repo.WithIDs("default", ac.ID)

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

func isImageAttachment(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}
