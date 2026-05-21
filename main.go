package main

import (
	"fmt"
	"go_app/ollama"
	"go_app/repository"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/ollama/ollama/api"
)

func main() {
	// --- 初期設定 ---
	token := "MTUwNDQ2MTE4NDgzNTkxMTg0MA.GyWdBx._QSAI0rAaWazwYUOketOIQzu1LMGv9VOv9Ichg"
	dsn := "postgres://haruto:your_password@localhost:5432/kanojo_memory?sslmode=disable"
	
	db, _ := sqlx.Connect("postgres", dsn)
	repo := repository.NewMemoryRepository(db)

	// Discord Botのセッション作成
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("Bot作成失敗: %v", err)
	}

	// メッセージが届いた時の処理（ハンドラ）を追加
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
    if m.Author.ID == s.State.User.ID { return }

    // --- 1. 最新の閾値とプロンプトをDBから取得 ---
    avgTStr := repo.GetSetting("avg_threshold", "0.38")
    maxTStr := repo.GetSetting("max_threshold", "0.50")
    sysPrompt := repo.GetSetting("system_prompt", "デフォルトの性格")

    avgT, _ := strconv.ParseFloat(avgTStr, 64)
    maxT, _ := strconv.ParseFloat(maxTStr, 64)

    // --- 2. 判定とID取得 ---
    userInput := m.Content
    userEmbedding := ollama.GetEmbedding(userInput)
    convID, _ := repo.GetOrCreateConversationID(userEmbedding, avgT, maxT)

    // --- 3. 短期記憶（5往復）の取得 ---
    pastMemories, _ := repo.GetRecentMemories(convID, 10)

   // --- 4. プロンプトの組み立て（Chat API用のメッセージ配列を作る） ---
    // strings.Builder をやめて、Ollamaのapi.Messageの配列にする
    var messages []api.Message

    // ① システムプロンプト（沙耶の設定）を role: "system" として入れる
    messages = append(messages, api.Message{
        Role:    "system",
        Content: sysPrompt,
    })

    // ② 短期記憶（過去の履歴）をループでそのまま role ごと追加する
    for _, mem := range pastMemories {
        messages = append(messages, api.Message{
            Role:    mem.Role,    // DBに保存されている "user" や "assistant" がそのまま入る
            Content: mem.Content,
        })
    }

    // ③ 今回の最新メッセージを role: "user" として追加する
    messages = append(messages, api.Message{
        Role:    "user",
        Content: userInput,
    })

    // --- 5. 生成と保存 ---
    aiResponse := ollama.GetChatResponse("qwen2.5:7b", messages)
    
    repo.SaveMemory(userInput, userEmbedding, "user", convID)
    aiEmbedding := ollama.GetEmbedding(aiResponse)
    repo.SaveMemory(aiResponse, aiEmbedding, "assistant", convID)

    // --- 6. 返信 ---
    reply := fmt.Sprintf("%s\n\n(ContextID: %s, Threshold: %v/%v)", aiResponse, convID, avgT, maxT)
    s.ChannelMessageSend(m.ChannelID, reply)
})

	// Botの起動
	err = dg.Open()
	if err != nil {
		log.Fatalf("接続失敗: %v", err)
	}

	fmt.Println("Botが起動しました。CTRL+Cで終了します。")
	
	// 終了信号を待機（これでプログラムが終了せずに動き続ける）
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	dg.Close()
}