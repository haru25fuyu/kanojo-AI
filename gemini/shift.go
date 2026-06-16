package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	jpholiday "github.com/rabitt1ove/jp-holidays"
)

// WorkBlock は1日の中の「勤務している時間帯」。
// 勤務以外の時間は自動的に自由（いつでも話せる）扱いなので、ここには持たない。
type WorkBlock struct {
	Start string `json:"start"` // "HH:MM"
	End   string `json:"end"`   // "HH:MM"（当日内。日をまたぐ夜勤は "24:00" で切る）
	Label string `json:"label"` // 早番・日勤・夜勤・通学 等（任意・空可）
}

// ShiftDayResult は1日ぶんの生成結果（投入順で返る）。
// OK=true は「正常にパースできた（書き込んでよい）」。勤務ゼロ＝休みの日でも OK=true。
type ShiftDayResult struct {
	Blocks []WorkBlock
	OK     bool
}

// ShiftBatchStatus はポーリング結果。
type ShiftBatchStatus struct {
	State   string           // JOB_STATE_*
	Done    bool             // operation の done フラグ
	Results []ShiftDayResult // SUCCEEDED 時のみ・投入順
	ErrMsg  string           // FAILED 時のエラーメッセージ
}

// SubmitShiftBatch は指定日付ぶんの勤務予定生成を inline バッチで投入し、ジョブ名(batches/xxx)を返す。
// 作成は非冪等なので、呼び出し側は返ったジョブ名を必ず記録し、二重投入しないこと。
func SubmitShiftBatch(ctx context.Context, model, charaName, systemPrompt string, dates []time.Time) (string, error) {
	if len(dates) == 0 {
		return "", fmt.Errorf("dates が空")
	}

	weekdays := []string{"日", "月", "火", "水", "木", "金", "土"}
	sysText := shiftSystemPrompt(charaName, systemPrompt)

	requests := make([]map[string]interface{}, 0, len(dates))
	for _, d := range dates {
		dateStr := d.Format("2006-01-02")
		dayInfo := weekdays[d.Weekday()] + "曜日"
		if name := jpholiday.HolidayName(d); name != "" {
			dayInfo += "・" + name + "【祝日】"
		}
		userText := fmt.Sprintf("対象日：%s（%s）\nこの日の「%s」の勤務予定をJSONで出力してください。", dateStr, dayInfo, charaName)

		genReq := map[string]interface{}{
			"system_instruction": map[string]interface{}{
				"parts": []map[string]interface{}{{"text": sysText}},
			},
			"contents": []map[string]interface{}{
				{
					"role":  "user",
					"parts": []map[string]interface{}{{"text": userText}},
				},
			},
			"generationConfig": map[string]interface{}{
				"responseMimeType": "application/json",
				"thinkingConfig":   map[string]interface{}{"thinkingBudget": 0},
			},
		}
		requests = append(requests, map[string]interface{}{
			"request":  genReq,
			"metadata": map[string]interface{}{"key": dateStr},
		})
	}

	payload := map[string]interface{}{
		"batch": map[string]interface{}{
			"display_name": fmt.Sprintf("shift-%s", time.Now().Format("20060102-150405")),
			"input_config": map[string]interface{}{
				"requests": map[string]interface{}{
					"requests": requests,
				},
			},
		},
	}

	url := fmt.Sprintf("%s/models/%s:batchGenerateContent", baseURL, model)
	body, err := callBatchAPI(ctx, http.MethodPost, url, payload)
	if err != nil {
		return "", err
	}

	var res struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("バッチ作成レスポンスのパース失敗: %w body=%s", err, string(body))
	}
	if res.Name == "" {
		return "", fmt.Errorf("バッチ作成: job name が空 body=%s", string(body))
	}
	return res.Name, nil
}

// PollShiftBatch はジョブ状態を取得する。SUCCEEDED なら inline 結果を投入順にパースして返す。
func PollShiftBatch(ctx context.Context, jobName string) (*ShiftBatchStatus, error) {
	url := fmt.Sprintf("%s/%s", baseURL, jobName) // jobName = "batches/xxx"
	body, err := callBatchAPI(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	var resp batchPollResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("ポーリングレスポンスのパース失敗: %w body=%s", err, string(body))
	}

	// state は実装差を吸収（.state / .metadata.state のどちらかに入る）
	state := resp.State
	if state == "" {
		state = resp.Metadata.State
	}

	status := &ShiftBatchStatus{State: state, Done: resp.Done}
	if resp.Error != nil {
		status.ErrMsg = resp.Error.Message
	}

	if state != "JOB_STATE_SUCCEEDED" {
		return status, nil
	}

	// 結果ブロックも実装差を吸収（.response / .dest のどちらか）
	block := resp.Response
	if block == nil || len(block.InlinedResponses) == 0 {
		if resp.Dest != nil {
			block = resp.Dest
		}
	}
	if block == nil {
		return status, nil
	}
	if len(block.InlinedResponses) == 0 && (block.ResponsesFile != "" || block.FileName != "") {
		// ファイル出力（inline<20MB 運用では発生しない想定）。今回は未対応。
		return status, fmt.Errorf("バッチ結果がファイル出力で返った（未対応）: %s%s", block.ResponsesFile, block.FileName)
	}

	for _, ir := range block.InlinedResponses {
		text := extractCandidateText(ir.Response)
		blocks, ok := parseWorkBlocks(text)
		status.Results = append(status.Results, ShiftDayResult{Blocks: blocks, OK: ok})
	}
	return status, nil
}

// ── 内部ヘルパ ──────────────────────────────────────────

func shiftSystemPrompt(charaName, systemPrompt string) string {
	return fmt.Sprintf(`あなたは「%s」というキャラクターの勤務予定（シフト）を設計するAIです。
以下のキャラクター設定を読み、指定された日にこの人物が「働いている時間帯」だけを出力してください。

【キャラクター設定】
%s

【ルール】
- 出力するのは勤務（仕事・バイト・通学など、拘束される時間）の時間帯のみ。睡眠や自由時間は出力しない（勤務以外は自動的に自由扱い）。
- 勤務がない日（休み）は空配列 [] にする。
- 1日に複数の勤務帯があってよい（中抜け・2本立てのシフト等）。なければ1本。
- 時刻は "HH:MM" の24時間表記。日をまたぐ夜勤は当日内の "24:00" までで切ること（翌日ぶんは別日として扱う）。
- label は補足（早番・遅番・日勤・夜勤・通学 など）を任意で入れてよい。空でもよい。
- 設定から職業・生活リズムを推測する。明確な職業がなければ人物像から自然な勤務パターンを作る（在宅・フリー・学生・無職などは勤務なしの日が多くてよい）。
- 曜日に応じて現実的に変える（平日と休日で違ってよい）。ただし大枠の生活リズムは崩しすぎない。
- 土日・祝日も考慮する。祝日は休みになりやすいが、接客・飲食・医療・サービス業など職種によっては通常どおり勤務する。職業から自然に判断すること。

以下のJSON形式のみを出力（前置き・マークダウン・説明は一切不要）：
{"work":[{"start":"08:30","end":"18:00","label":"日勤"}]}
（休みの日の例：{"work":[]}）`, charaName, systemPrompt)
}

func extractCandidateText(r *genContentResp) string {
	if r == nil || len(r.Candidates) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, p := range r.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// parseWorkBlocks は勤務帯JSONをパースする。
// 第2返り値 ok=false はパース失敗（その日は書き込まず再生成に回す）。
// 勤務ゼロ（"work":[]＝休み）は ok=true で空スライスを返す。
func parseWorkBlocks(text string) ([]WorkBlock, bool) {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 {
		return nil, false
	}
	var wrap struct {
		Work []WorkBlock `json:"work"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &wrap); err != nil {
		return nil, false
	}
	return wrap.Work, true
}

// callBatchAPI は Batch API 用の HTTP 呼び出し（x-goog-api-key ヘッダ方式）。
// payload が nil の場合はボディなし（GET 用）。
func callBatchAPI(ctx context.Context, method, url string, payload interface{}) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	apiKey := os.Getenv("GEMINI_API_KEY")

	var reqBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("レスポンス読み込み失敗: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Batch APIエラー ステータス:%d ボディ:%s", resp.StatusCode, string(body))
	}
	return body, nil
}

// ── ポーリングレスポンスのパース用構造体（実装差を吸収するため両系統を持つ）──

type batchPollResp struct {
	Name     string `json:"name"`
	Done     bool   `json:"done"`
	State    string `json:"state"` // batches 系
	Metadata struct {
		State string `json:"state"` // operation 系
	} `json:"metadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Response *batchResultBlock `json:"response"` // operation 系の結果
	Dest     *batchResultBlock `json:"dest"`     // batches 系の結果
}

type batchResultBlock struct {
	InlinedResponses []struct {
		Response *genContentResp `json:"response"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"inlinedResponses"`
	ResponsesFile string `json:"responsesFile"`
	FileName      string `json:"fileName"`
}

type genContentResp struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}
