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

func (r *MemoryRepository) UpsertSchedule(label string, date time.Time, repeat bool) error {
	query := `
		INSERT INTO schedules (user_id, character_id, label, date, repeat, notified)
		VALUES ($1, $2, $3, $4, $5, FALSE)
		ON CONFLICT DO NOTHING`
	_, err := r.db.Exec(query, r.UserID, r.CharacterID, label, date, repeat)
	return err
}

func (r *MemoryRepository) GetTodaySchedules() ([]Schedule, error) {
	var schedules []Schedule
	query := `
		SELECT id, label, date, repeat, notified, created_at
		FROM schedules
		WHERE user_id = $1 AND character_id = $2 AND (
			date = CURRENT_DATE
			OR date = CURRENT_DATE + INTERVAL '1 day'
			OR (repeat = TRUE AND to_char(date, 'MM-DD') = to_char(CURRENT_DATE, 'MM-DD'))
			OR (repeat = TRUE AND to_char(date, 'MM-DD') = to_char(CURRENT_DATE + INTERVAL '1 day', 'MM-DD'))
		)
		AND notified = FALSE`
	err := r.db.Select(&schedules, query, r.UserID, r.CharacterID)
	return schedules, err
}

func (r *MemoryRepository) MarkNotified(id int64, repeat bool) error {
	if repeat {
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

func (r *MemoryRepository) GetAllSchedules() ([]Schedule, error) {
	var schedules []Schedule
	err := r.db.Select(&schedules, `
		SELECT id, label, date, repeat, notified, created_at
		FROM schedules
		WHERE user_id = $1 AND character_id = $2
		ORDER BY date ASC`, r.UserID, r.CharacterID)
	return schedules, err
}
