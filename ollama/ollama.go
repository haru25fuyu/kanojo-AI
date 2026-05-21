package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/ollama/ollama/api"
)

const baseURL = "http://localhost:11434/api"

// GetResponse は Ollama の通常のチャット返答を取得する
func GetResponse(model string, messages []api.Message) string {
    url := baseURL + "/chat" // エンドポイントは /chat にする
    payload := map[string]interface{}{
        "model":    model,
        "messages": messages, // 配列をそのまま渡す
        "stream":   false,
    }
    
    // 自作の callOllama を呼ぶ
    return callOllama(url, payload, "message")
}

// GetEmbedding は文章をベクトル数値に変換する
func GetEmbedding(text string) []float64 {
	url := baseURL + "/embeddings"
	payload := map[string]interface{}{
		"model":  "nomic-embed-text",
		"prompt": text,
	}

	resultBody := callOllamaRaw(url, payload)
	var resp struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal(resultBody, &resp); err != nil {
		log.Printf("ベクトルパース失敗: %v", err) // サービスを止めないようPrintfに
		return nil
	}
	return resp.Embedding
}

// callOllamaRaw は内部的なHTTPリクエストの共通処理 (小文字で非公開に)
func callOllamaRaw(url string, payload interface{}) []byte {
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Fatalf("Ollama接続失敗: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body
}

func callOllama(url string, payload interface{}, key string) string {
	body := callOllamaRaw(url, payload)
	var res map[string]interface{}
	json.Unmarshal(body, &res)
	
	val, ok := res[key].(string)
	if !ok {
		return ""
	}
	return val
}

// GetChatResponse は、Chat API（Roleごとの配列）を使ってモデルから返答を取得する
func GetChatResponse(modelName string, messages []api.Message) string {
	// Ollamaのデフォルトクライアントを作成（ローカルの localhost:11434 に接続）
	client, err := api.ClientFromEnvironment()
	if err != nil {
		log.Printf("Ollamaクライアント作成失敗: %v", err)
		return "（裏側でエラーが起きてるみたいよ……っ）"
	}

	streamFlag := false

	ctx := context.Background()
	req := &api.ChatRequest{
		Model:    modelName,
		Messages: messages,
		Stream:   &streamFlag,
	}

	var aiResponse string

	// OllamaのChat APIを呼び出す
	err = client.Chat(ctx, req, func(resp api.ChatResponse) error {
		aiResponse = resp.Message.Content
		return nil
	})

	if err != nil {
		log.Printf("Ollama生成失敗: %v", err)
		return "（通信エラーかしら、あんた何したのよ）"
	}

	return aiResponse
}