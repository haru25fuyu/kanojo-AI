package repository

import "time"

type UserProfile struct {
	UserID    string    `db:"user_id"`
	Name      string    `db:"name"`
	Age       *int      `db:"age"`
	Gender    string    `db:"gender"`
	Job       string    `db:"job"`
	UpdatedAt time.Time `db:"updated_at"`
}

// GetUserProfile はユーザーのコア情報を取得する
func (r *MemoryRepository) GetUserProfile() (*UserProfile, error) {
	var profile UserProfile
	err := r.db.Get(&profile, `
		SELECT user_id, name, age, gender, job, updated_at
		FROM user_profile
		WHERE user_id = $1`, r.UserID)
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

// UpsertUserProfile はユーザーのコア情報を保存・更新する
func (r *MemoryRepository) UpsertUserProfile(name string, age *int, gender string, job string) error {
	query := `
		INSERT INTO user_profile (user_id, name, age, gender, job, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			name       = COALESCE(NULLIF(EXCLUDED.name, ''),       user_profile.name),
			age        = COALESCE(EXCLUDED.age,                    user_profile.age),
			gender     = COALESCE(NULLIF(EXCLUDED.gender, ''),     user_profile.gender),
			job        = COALESCE(NULLIF(EXCLUDED.job, ''),        user_profile.job),
			updated_at = NOW()`
	_, err := r.db.Exec(query, r.UserID, name, age, gender, job)
	return err
}
