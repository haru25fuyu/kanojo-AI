package main

import (
	"context"
	"fmt"
	"go_app/gemini"
	"go_app/repository"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"go_app/functions"

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

// trustGainFor は好感度が信頼度を引き離しているほど、
// 信頼度が追いつくように速く上がる動的な信頼度増分を返す。
// （normal返答のときだけ使う想定。short/skipはプリセットのまま）
func trustGainFor(affection, trust int) int {
	gain := 1 + (affection-trust)/3000
	if gain < 0 {
		gain = 0 // 信頼度が好感度を追い越した時は据え置き
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

	go functions.RunNightlyBatchLoop(repo)

	activeChars, _ := repo.GetActiveCharacters()

	for _, c := range activeChars {
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

		// このリクエスト専用のrepo。共有repoは書き換えない（多人数で混ざらないように）
		r := repo.WithIDs(m.Author.ID, charaID)

		// 1. 設定取得（GetSettingはID不要なのでrepoのまま）
		avgTStr := repo.GetSetting("avg_threshold", "0.38")
		maxTStr := repo.GetSetting("max_threshold", "0.50")
		topicTStr := repo.GetSetting("topic_threshold", "0.50")
		rulePrompt := repo.GetSetting("system_prompt_rule", "日常会話に徹してください。")
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

		// 2. 会話ID取得
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
			}

			// 新規話題のときだけ即時要約
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

		// 5. ステータス・段階・プロンプト組み立て
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

		var profile *gemini.UserProfile
		if prof, err := r.GetUserProfile(); err == nil && prof != nil {
			profile = &gemini.UserProfile{
				Name:   prof.Name,
				Age:    prof.Age,
				Gender: prof.Gender,
				Job:    prof.Job,
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

		messages := gemini.BuildBaseMessages(gemini.BaseMessagesParams{
			RulePrompt:   rulePrompt,
			CharaPrompt:  charaPrompt,
			StagePrompt:  stagePrompt,
			Profile:      profile,
			UserInfos:    userInfoEntries,
			Topics:       topicEntries,
			PastMessages: pastMsgs,
		})

		messages = append(messages, gemini.Message{
			Role:    "user",
			Content: userInput,
		})

		var statusText string
		if status != nil {
			statusText = fmt.Sprintf(
				"【現在のパートナーステータス】\n好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d\nこのステータスに基づいて返答してください。",
				status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy,
			)
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

		// 8. 返信を先に送る（保存は後で goroutine に任せる）
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

		// 9. 計測ログを debug_mode 時に Discord に送る
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

		// 10. ステータス変動を確定してから保存・更新を goroutine に逃がす
		//   - Affection += 1 は元のまま（意図不明なので残置）
		//   - 信頼度は normal のときだけ「好感度との差」で加速させる
		finalDelta := repository.StatusDelta(chatResp.Delta)
		finalDelta.Affection += 1
		if chatResp.ReplyType == "normal" && status != nil {
			finalDelta.Trust = trustGainFor(status.Affection, status.Trust)
		}

		go func(
			input string,
			inputEmb []float64,
			response string,
			cid string,
			delta repository.StatusDelta,
		) {
			r.SaveMemory(input, inputEmb, "user", cid)

			aiEmb := gemini.GetEmbedding(response)
			r.SaveMemory(response, aiEmb, "assistant", cid)

			if err := r.ApplyStatusDelta(delta); err != nil {
				log.Printf("ステータス更新失敗: %v", err)
			}
		}(userInput, userEmbedding, aiResponse, convID, finalDelta)
	})

	if err = dg.Open(); err != nil {
		log.Fatalf("接続失敗: %v", err)
	}
	defer dg.Close()

	for _, c := range activeChars {
		go functions.RunProactiveLoop(repo, dg, c)
	}
	go functions.RunScheduleLoop(repo, dg)

	fmt.Println("Botが起動しました。CTRL+Cで終了します。")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}
