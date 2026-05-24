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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

// lastConvState は直前の会話状態を保持する（conversation切り替わり検知用）
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

	// アクティブなキャラ一覧を取得
	activeChars, _ := repo.GetActiveCharacters()

	// キャラごとにイベントループを起動
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

		// チャンネルIDからキャラIDを決定
		log.Printf("受信チャンネルID: %s", m.ChannelID)
		charaID := repo.GetCharacterIDByChannel(m.ChannelID)
		log.Printf("キャラID: %s", charaID)

		// グループチャットは将来実装
		if charaID == "group" {
			return
		}

		// ユーザーIDとキャラIDをリポジトリにセット
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

			// conversation切り替わり時 → 直前のconversationを要約
			state.mu.Lock()
			prevConvID := state.convID
			prevTopicID := state.topicID
			state.convID = convID
			state.topicID = topicID
			state.mu.Unlock()

			if isNewConv && prevConvID != "" {
				// 直前の会話からユーザー情報を抽出（非同期）
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
					for _, item := range result.CharaInfo {
						emb := gemini.GetEmbedding(item.Key + ": " + item.Value)
						if err := r.UpsertCharaInfo(item.Key, item.Value, item.Importance, emb); err != nil {
							log.Printf("キャラ情報保存失敗: %v", err)
						}
					}
					log.Printf("情報抽出完了: ユーザー%d件 キャラ%d件", len(result.UserInfo), len(result.CharaInfo))
				}(prevConvID, modelBatch, repo.UserID, repo.CharacterID)

				// 直前の会話からスケジュール・記念日を抽出（非同期）
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
		// convIDをまたいで直近10件取得
		// 10件全部同じconversationなら全件取得に切り替え
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
		var messages []gemini.Message

		// ① 現在時刻
		messages = append(messages, gemini.Message{
			Role: "system",
			Content: func() string {
				weekdays := []string{"日", "月", "火", "水", "木", "金", "土"}
				now := time.Now()
				return fmt.Sprintf("【現在時刻】%s（%s）%s", now.Format("2006/01/02"), weekdays[now.Weekday()], now.Format("15:04"))
			}(),
		})

		// ② 絶対ルール（どのキャラでも共通）
		messages = append(messages, gemini.Message{
			Role:    "system",
			Content: rulePrompt,
		})

		// ② キャラ設定
		messages = append(messages, gemini.Message{
			Role:    "system",
			Content: charaPrompt,
		})

		// ② ユーザー情報をプロンプトに注入
		userInfoLimit, _ := strconv.Atoi(repo.GetSetting("user_info_limit", "5"))
		topUserInfos, _ := repo.GetTopUserInfo(userInfoLimit)
		if len(topUserInfos) > 0 {
			var userInfoText strings.Builder
			userInfoText.WriteString("【ユーザーについて知っていること】\n")
			for _, info := range topUserInfos {
				fmt.Fprintf(&userInfoText, "- %s: %s\n", info.Key, info.Value)
			}
			messages = append(messages, gemini.Message{
				Role:    "system",
				Content: userInfoText.String(),
			})
		}

		// ② 2段階検索で記憶を取得してプロンプトに注入
		// まずキーワード検索（軽量）、なければベクトル検索（詳細）
		topTopics, _ := repo.GetTopTopics(3)
		if len(topTopics) == 0 {
			topTopics, _ = repo.SearchTopicsByEmbedding(userEmbedding, topicT)
		}
		if len(topTopics) > 0 {
			var memoryText strings.Builder
			memoryText.WriteString("【あなたが覚えていること】\n")
			for _, t := range topTopics {
				fmt.Fprintf(&memoryText, "- %s（熱量: %.1f）\n", t.Summary, t.Heat)
			}
			messages = append(messages, gemini.Message{
				Role:    "system",
				Content: memoryText.String(),
			})
		}

		// ③ パートナーの生活イベント（直近3件 + 関連イベント検索）
		recentEvents, _ := repo.GetRecentEvents(3)
		relatedEvents, _ := repo.SearchEvents(userEmbedding, 3)

		// 重複除去してまとめる
		eventMap := map[int64]repository.PartnerEvent{}
		for _, e := range recentEvents {
			eventMap[e.ID] = e
		}
		for _, e := range relatedEvents {
			eventMap[e.ID] = e
		}
		if len(eventMap) > 0 {
			var eventText strings.Builder
			eventText.WriteString("【最近あったこと】\n")
			for _, e := range eventMap {
				fmt.Fprintf(&eventText, "- %s（%s）\n", e.Event, e.CreatedAt.Format("1/2 15:04"))
			}
			messages = append(messages, gemini.Message{
				Role:    "system",
				Content: eventText.String(),
			})
		}

		// ③ 短期記憶（時刻付き）
		now := time.Now()
		for _, mem := range pastMemories {
			diff := now.Sub(mem.CreatedAt)
			var timeLabel string
			switch {
			case diff < 5*time.Minute:
				timeLabel = "さっき"
			case diff < time.Hour:
				timeLabel = fmt.Sprintf("%d分前", int(diff.Minutes()))
			case diff < 24*time.Hour:
				timeLabel = fmt.Sprintf("%d時間前", int(diff.Hours()))
			case diff < 7*24*time.Hour:
				timeLabel = fmt.Sprintf("%d日前", int(diff.Hours()/24))
			default:
				timeLabel = mem.CreatedAt.Format("1/2")
			}
			messages = append(messages, gemini.Message{
				Role:    mem.Role,
				Content: fmt.Sprintf("(%s) %s", timeLabel, mem.Content),
			})
		}

		// ④ 今回の発言
		messages = append(messages, gemini.Message{
			Role:    "user",
			Content: userInput,
		})

		// 6. ステータス取得してプロンプトに混ぜる
		status, _ := repo.GetPartnerStatus()
		var statusText string
		if status != nil {
			statusText = fmt.Sprintf(
				"【現在のパートナーステータス】\n好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d\nこのステータスに基づいて返答してください。",
				status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy,
			)
		}

		// 7. 生成
		chatResp, err := gemini.GetChatResponseWithStatus(context.Background(), modelChat, messages, statusText)
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

		// 8. 保存（成功時のみ）
		repo.SaveMemory(userInput, userEmbedding, "user", convID)
		aiEmbedding := gemini.GetEmbedding(aiResponse)
		repo.SaveMemory(aiResponse, aiEmbedding, "assistant", convID)

		// ステータスを更新（単純接触効果で毎回+1）
		delta := repository.StatusDelta(chatResp.Delta)
		delta.Affection += 1
		if err := repo.ApplyStatusDelta(delta); err != nil {
			log.Printf("ステータス更新失敗: %v", err)
		}

		// 9. 返信
		debugMode := repo.GetSetting("debug_mode", "false")
		var reply string
		if debugMode == "true" && status != nil {
			reply = fmt.Sprintf("%s\n\n(Conv: %s | Topic: %s | 好感度:%d 気分:%d 活力:%d)",
				aiResponse, convID, topicID, status.Affection, status.Mood, status.Energy)
		} else {
			reply = aiResponse
		}
		s.ChannelMessageSend(m.ChannelID, reply)
	})

	if err = dg.Open(); err != nil {
		log.Fatalf("接続失敗: %v", err)
	}
	defer dg.Close()

	// アクティブなキャラごとに自発メッセージループを起動
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

// runEventLoop は1〜2時間ごとにパートナーの生活イベントを生成する
func runEventLoop(repo *repository.MemoryRepository, chara repository.Character) {
	for {
		time.Sleep(90 * time.Minute)

		repo.CharacterID = chara.ID
		status, err := repo.GetPartnerStatus()
		if err != nil {
			log.Printf("イベント生成: ステータス取得失敗: %v", err)
			continue
		}

		modelBatch := repo.GetSetting("model_batch", "gemini-3.1-flash-lite")
		statusText := fmt.Sprintf("好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d",
			status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy)

		charaData, _ := repo.GetCharacter(chara.ID)
		var charaPrompt string
		if charaData != nil {
			charaPrompt = charaData.SystemPrompt
		}
		result, err := gemini.GenerateEvent(context.Background(), modelBatch, statusText, time.Now().Hour(), charaPrompt)
		if err != nil || result == nil {
			log.Printf("イベント生成失敗: %v", err)
			continue
		}

		// イベントをembeddingして保存
		embedding := gemini.GetEmbedding(result.Event)
		if err := repo.SaveEvent(result.Event, embedding); err != nil {
			log.Printf("イベント保存失敗: %v", err)
			continue
		}

		// ステータスに反映
		if err := repo.ApplyStatusDelta(repository.StatusDelta(result.Delta)); err != nil {
			log.Printf("イベントステータス更新失敗: %v", err)
			continue
		}

		log.Printf("イベント発生: %s", result.Event)
	}
}

// runProactiveLoop は定期的に自発メッセージを送るか判断する
func runProactiveLoop(repo *repository.MemoryRepository, dg *discordgo.Session, chara repository.Character) {
	// 起動直後は少し待つ
	time.Sleep(10 * time.Minute)

	for {
		// 30分ごとにチェック
		time.Sleep(30 * time.Minute)

		// 最後のメッセージ時刻を取得
		lastTime, err := repo.GetLastMessageTime()
		if err != nil {
			continue
		}

		elapsed := time.Since(lastTime)

		// 1時間以上経過してなければスキップ
		if elapsed < time.Hour {
			continue
		}

		// 必要な情報を集める
		status, err := repo.GetPartnerStatus()
		if err != nil {
			continue
		}
		statusText := fmt.Sprintf("好感度:%d 信頼度:%d 疲労度:%d 気分:%d ストレス:%d 活力:%d",
			status.Affection, status.Trust, status.Fatigue, status.Mood, status.Stress, status.Energy)

		lastMsg, _ := repo.GetLastMemory()

		recentEvents, _ := repo.GetRecentEvents(3)
		var eventTexts []string
		for _, e := range recentEvents {
			eventTexts = append(eventTexts, e.Event)
		}

		hotTopics, _ := repo.GetTopTopics(3)
		var topicTexts []string
		for _, t := range hotTopics {
			topicTexts = append(topicTexts, fmt.Sprintf("%s（熱量:%.1f）", t.Summary, t.Heat))
		}

		// 経過時間を自然な文字列に
		var elapsedText string
		switch {
		case elapsed < 2*time.Hour:
			elapsedText = fmt.Sprintf("%.0f分", elapsed.Minutes())
		case elapsed < 24*time.Hour:
			elapsedText = fmt.Sprintf("%.0f時間", elapsed.Hours())
		default:
			elapsedText = fmt.Sprintf("%.0f日", elapsed.Hours()/24)
		}

		modelChat := repo.GetSetting("model_chat", "gemini-3-flash-preview")
		charaPrompt := chara.SystemPrompt
		channelID := chara.ProactiveChannel
		if channelID == "" {
			log.Printf("キャラ[%s]のproactive_channelが未設定", chara.ID)
			continue
		}

		// 時間帯チェック（デフォルト8時〜22時のみ送信）
		hourStart, _ := strconv.Atoi(repo.GetSetting("proactive_hour_start", "8"))
		hourEnd, _ := strconv.Atoi(repo.GetSetting("proactive_hour_end", "22"))
		currentHour := time.Now().Hour()
		if currentHour < hourStart || currentHour >= hourEnd {
			log.Printf("自発メッセージ: 時間帯外（%d時）スキップ", currentHour)
			continue
		}

		result, err := gemini.GenerateProactiveMessage(context.Background(), modelChat, gemini.ProactivePayload{
			ElapsedTime:  elapsedText,
			Status:       statusText,
			LastMessage:  lastMsg,
			RecentEvents: eventTexts,
			HotTopics:    topicTexts,
			CharaPrompt:  charaPrompt,
		})
		if err != nil || result == nil || !result.Send {
			log.Printf("自発メッセージ: 見送り")
			continue
		}

		dg.ChannelMessageSend(channelID, result.Message)
		log.Printf("自発メッセージ送信: %s", result.Message)

		// 自発メッセージを履歴に保存
		repo.CharacterID = chara.ID
		repo.UserID = "default"
		embedding := gemini.GetEmbedding(result.Message)
		// 最新のconversation_idを取得して保存
		var convID string
		repo.DB().Get(&convID, `SELECT id FROM conversations WHERE character_id = $1 ORDER BY updated_at DESC LIMIT 1`, chara.ID)
		if convID != "" {
			repo.SaveMemory(result.Message, embedding, "assistant", convID)
		}
	}
}

// runScheduleLoop は1時間ごとにスケジュール・記念日をチェックして通知する
func runScheduleLoop(repo *repository.MemoryRepository, dg *discordgo.Session) {
	for {
		time.Sleep(1 * time.Hour)

		// キャラごとのチャンネルに送信
		activeChars, err := repo.GetActiveCharacters()
		if err != nil || len(activeChars) == 0 {
			continue
		}

		schedules, err := repo.GetTodaySchedules()
		if err != nil || len(schedules) == 0 {
			continue
		}

		modelChat := repo.GetSetting("model_chat", "gemini-3-flash-preview")
		chara, _ := repo.GetCharacter(repo.CharacterID)
		var charaPrompt string
		if chara != nil {
			charaPrompt = chara.SystemPrompt
		}
		status, _ := repo.GetPartnerStatus()
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
				{Role: "system", Content: charaPrompt},
				{Role: "system", Content: fmt.Sprintf("現在のステータス: %s", statusText)},
				{
					Role: "user",
					Content: fmt.Sprintf(
						"%sは「%s」です。これに関して自然に一言話しかけてください。",
						timing, s.Label,
					),
				},
			}

			for _, ac := range activeChars {
				if ac.ProactiveChannel == "" {
					continue
				}
				reply := gemini.GetChatResponse(modelChat, messages)
				dg.ChannelMessageSend(ac.ProactiveChannel, reply)
			}
			repo.MarkNotified(s.ID, s.Repeat)
			log.Printf("スケジュール通知: %s（%s）", s.Label, timing)
		}
	}
}
