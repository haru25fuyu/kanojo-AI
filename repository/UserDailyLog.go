package repository

import "time"

type UserDailyLog struct {
	ID          int64     `db:"id"`
	UserID      string    `db:"user_id"`
	CharacterID string    `db:"character_id"`
	Event       string    `db:"event"`
	Date        time.Time `db:"date"`
	CreatedAt   time.Time `db:"created_at"`
}

// GetTodayDailyLog は今日のユーザー行動ログを取得する
func (r *MemoryRepository) GetTodayDailyLog() ([]UserDailyLog, error) {
	var logs []UserDailyLog
	err := r.db.Select(&logs, `
		SELECT id, user_id, character_id, event, date, created_at
		FROM user_daily_log
		WHERE user_id = $1 AND character_id = $2
		AND date = CURRENT_DATE
		ORDER BY created_at ASC`,
		r.UserID, r.CharacterID)
	return logs, err
}

// InsertDailyActivities は今日の行動を一括挿入する（同一テキストは無視）
func (r *MemoryRepository) InsertDailyActivities(events []string) error {
	for _, event := range events {
		if event == "" {
			continue
		}
		_, err := r.db.Exec(`
			INSERT INTO user_daily_log (user_id, character_id, event, date)
			VALUES ($1, $2, $3, CURRENT_DATE)
			ON CONFLICT (user_id, character_id, event, date) DO NOTHING`,
			r.UserID, r.CharacterID, event)
		if err != nil {
			return err
		}
	}
	return nil
}
