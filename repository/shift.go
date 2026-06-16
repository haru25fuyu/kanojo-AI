package repository

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go_app/gemini"

	"github.com/lib/pq"
)

// ShiftBatchJob は処理中のシフト生成バッチジョブ1件。
type ShiftBatchJob struct {
	ID          int
	CharacterID string
	JobName     string
	Dates       []time.Time // 投入順（PollShiftBatch の結果と index 対応）
	Status      string
}

// UpsertCharacterShift は1日ぶんの勤務帯を保存・更新する。
// 休みの日は blocks が空でよい（空配列の行＝「生成済みの休み」として扱う）。
func (r *MemoryRepository) UpsertCharacterShift(date time.Time, blocks []gemini.WorkBlock) error {
	b, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`
		INSERT INTO character_shift (character_id, date, bands, created_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (character_id, date) DO UPDATE SET
			bands      = EXCLUDED.bands,
			created_at = NOW()`,
		r.CharacterID, date.Format("2006-01-02"), string(b))
	return err
}

// GetWorkBlocks は指定日の勤務帯を取得する（行が無ければ sql.ErrNoRows）。
// 行があり空スライスなら「休みの日」。
func (r *MemoryRepository) GetWorkBlocks(date time.Time) ([]gemini.WorkBlock, error) {
	var raw []byte
	err := r.db.Get(&raw, `
		SELECT bands FROM character_shift
		WHERE character_id = $1 AND date = $2`,
		r.CharacterID, date.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	var blocks []gemini.WorkBlock
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return nil, err
		}
	}
	return blocks, nil
}

// CountFutureShiftDays は from 以降の生成済み日数を返す（休みの日も1日と数える）。
func (r *MemoryRepository) CountFutureShiftDays(from time.Time) (int, error) {
	var n int
	err := r.db.Get(&n, `
		SELECT COUNT(*) FROM character_shift
		WHERE character_id = $1 AND date >= $2`,
		r.CharacterID, from.Format("2006-01-02"))
	return n, err
}

// MissingShiftDates は [from, from+horizonDays) のうち未生成の日付を投入順（昇順）で返す。
func (r *MemoryRepository) MissingShiftDates(from time.Time, horizonDays int) ([]time.Time, error) {
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	end := fromDate.AddDate(0, 0, horizonDays)

	var existing []time.Time
	err := r.db.Select(&existing, `
		SELECT date FROM character_shift
		WHERE character_id = $1 AND date >= $2 AND date < $3`,
		r.CharacterID, fromDate.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(existing))
	for _, d := range existing {
		have[d.Format("2006-01-02")] = true
	}

	var missing []time.Time
	for d := fromDate; d.Before(end); d = d.AddDate(0, 0, 1) {
		if !have[d.Format("2006-01-02")] {
			missing = append(missing, d)
		}
	}
	return missing, nil
}

// InsertShiftBatchJob はバッチジョブを pending で記録する。
func (r *MemoryRepository) InsertShiftBatchJob(jobName string, dates []time.Time) error {
	ds := make([]string, len(dates))
	for i, d := range dates {
		ds[i] = d.Format("2006-01-02")
	}
	_, err := r.db.Exec(`
		INSERT INTO shift_batch_jobs (character_id, job_name, dates, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'pending', NOW(), NOW())`,
		r.CharacterID, jobName, pq.Array(ds))
	return err
}

// HasPendingShiftBatchJob はこのキャラに処理中ジョブがあるかを返す（二重投入防止）。
func (r *MemoryRepository) HasPendingShiftBatchJob() (bool, error) {
	var n int
	err := r.db.Get(&n, `
		SELECT COUNT(*) FROM shift_batch_jobs
		WHERE character_id = $1 AND status = 'pending'`,
		r.CharacterID)
	return n > 0, err
}

// GetPendingShiftBatchJobs は全キャラの処理中ジョブを返す（ポーリング用・キャラ非依存）。
func (r *MemoryRepository) GetPendingShiftBatchJobs() ([]ShiftBatchJob, error) {
	type row struct {
		ID          int            `db:"id"`
		CharacterID string         `db:"character_id"`
		JobName     string         `db:"job_name"`
		Dates       pq.StringArray `db:"dates"`
		Status      string         `db:"status"`
	}
	var rows []row
	err := r.db.Select(&rows, `
		SELECT id, character_id, job_name, dates, status
		FROM shift_batch_jobs
		WHERE status = 'pending'
		ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}

	jobs := make([]ShiftBatchJob, 0, len(rows))
	for _, rw := range rows {
		var dates []time.Time
		for _, ds := range rw.Dates {
			if t, perr := time.Parse("2006-01-02", ds); perr == nil {
				dates = append(dates, t)
			}
		}
		jobs = append(jobs, ShiftBatchJob{
			ID:          rw.ID,
			CharacterID: rw.CharacterID,
			JobName:     rw.JobName,
			Dates:       dates,
			Status:      rw.Status,
		})
	}
	return jobs, nil
}

// UpdateShiftBatchJobStatus はジョブのステータスを更新する（done / failed 等）。
func (r *MemoryRepository) UpdateShiftBatchJobStatus(id int, status string) error {
	_, err := r.db.Exec(`
		UPDATE shift_batch_jobs SET status = $1, updated_at = NOW() WHERE id = $2`,
		status, id)
	return err
}

// ResolveShiftText は注入用テキストを勤務帯から導出する。
// ok=false はシフト未生成、または「勤務中でも休みでもなく単に空いている」状態（注入しない）。
// 未生成は正常系なのでエラーにはしない。
func (r *MemoryRepository) ResolveShiftText(now time.Time) (string, bool, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	blocks, err := r.GetWorkBlocks(today)
	if err != nil {
		return "", false, nil // 行なし（未生成）→ 注入しない
	}

	nowMin := now.Hour()*60 + now.Minute()

	// 1) いま勤務中か
	for _, b := range blocks {
		s, e := parseHHMM(b.Start), parseHHMM(b.End)
		if s < 0 || e < 0 {
			continue
		}
		if nowMin >= s && nowMin < e {
			return workingNowPhrase(b), true, nil
		}
	}

	// 2) このあと今日まだ勤務があるか（直近の開始を探す）
	nextMin := -1
	var nextStart, nextLabel string
	for _, b := range blocks {
		s := parseHHMM(b.Start)
		if s > nowMin && (nextMin == -1 || s < nextMin) {
			nextMin = s
			nextStart = b.Start
			nextLabel = b.Label
		}
	}
	if nextMin >= 0 {
		return upcomingWorkPhrase(nextStart, nextLabel), true, nil
	}

	// 3) 今日もう勤務はない
	if len(blocks) == 0 {
		return "今日は仕事が休み", true, nil // 終日オフ
	}
	return "", false, nil // 今日の勤務は終わった → 特に注入しない（普通に自由）
}

func parseHHMM(s string) int {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return -1
	}
	return h*60 + m
}

// workingNowPhrase は勤務中を「今は仕事中（日勤・18:00まで）」のように整形する。
func workingNowPhrase(b gemini.WorkBlock) string {
	var detail []string
	if b.Label != "" {
		detail = append(detail, b.Label)
	}
	if end := strings.TrimSpace(b.End); end != "" && end != "24:00" {
		detail = append(detail, end+"まで")
	}
	if len(detail) > 0 {
		return "今は仕事中（" + strings.Join(detail, "・") + "）"
	}
	return "今は仕事中"
}

// upcomingWorkPhrase はこのあとの勤務を「このあと18:00から仕事（日勤）」のように整形する。
func upcomingWorkPhrase(start, label string) string {
	if label != "" {
		return "このあと" + start + "から仕事（" + label + "）"
	}
	return "このあと" + start + "から仕事"
}
