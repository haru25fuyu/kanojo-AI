package discord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// DiscordWebhookResponse はDiscordに送るJSON構造体
type DiscordWebhookResponse struct {
	Content string `json:"content"`
}

// sendToDiscord は指定したURLにメッセージを飛ばす
func Send(webhookURL, message string) error {
	payload := DiscordWebhookResponse{
		Content: message,
	}
	
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("discord returned non-204 status: %d", resp.StatusCode)
	}

	return nil
}