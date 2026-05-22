package repository

import "time"

type Schedule struct {
	ID        int64     `db:"id"`
	Label     string    `db:"label"`
	Date      time.Time `db:"date"`
	Repeat    bool      `db:"repeat"`
	Notified  bool      `db:"notified"`
	CreatedAt time.Time `db:"created_at"`
}

// UpsertSchedule はスケジュール・記念日を保存する（同じlabelなら上書き）
func (r *MemoryRepository) UpsertSchedule(label string, date time.Time, repeat bool) error {
	query := `
		INSERT INTO schedules (user_id, label, date, repeat, notified)
		VALUES ($1, $2, $3, $4, FALSE)
		ON CONFLICT DO NOTHING`
	_, err := r.db.Exec(query, r.UserID, label, date, repeat)
	return err
}

// GetTodaySchedules は今日・明日のスケジュールを取得する（通知チェック用）
func (r *MemoryRepository) GetTodaySchedules() ([]Schedule, error) {
	var schedules []Schedule
	query := `
		SELECT id, label, date, repeat, notified, created_at
		FROM schedules
		WHERE user_id = $1 AND (
			-- 今日のスケジュール
			date = CURRENT_DATE
			OR
			-- 明日の予告（前日通知）
			date = CURRENT_DATE + INTERVAL '1 day'
			OR
			-- 繰り返し記念日（月日が一致）
			(repeat = TRUE AND to_char(date, 'MM-DD') = to_char(CURRENT_DATE, 'MM-DD'))
			OR
			-- 繰り返し記念日の前日予告
			(repeat = TRUE AND to_char(date, 'MM-DD') = to_char(CURRENT_DATE + INTERVAL '1 day', 'MM-DD'))
		)
		AND notified = FALSE`
	err := r.db.Select(&schedules, query, r.UserID)
	return schedules, err
}

// MarkNotified は通知済みにする（repeatのものはリセット）
func (r *MemoryRepository) MarkNotified(id int64, repeat bool) error {
	if repeat {
		// 繰り返しの場合は来年の日付に更新してnotifiedをリセット
		_, err := r.db.Exec(`
			UPDATE schedules SET
				date     = date + INTERVAL '1 year',
				notified = FALSE
			WHERE id = $1`, id)
		return err
	}
	_, err := r.db.Exec(`UPDATE schedules SET notified = TRUE WHERE id = $1`, id)
	return err
}

// GetAllSchedules は全スケジュールを取得する（管理用）
func (r *MemoryRepository) GetAllSchedules() ([]Schedule, error) {
	var schedules []Schedule
	err := r.db.Select(&schedules, `SELECT id, label, date, repeat, notified, created_at FROM schedules ORDER BY date ASC`)
	return schedules, err
}
