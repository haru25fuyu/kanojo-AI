package repository

import "time"

// CharaProfile はキャラクターのコア情報（名前・年齢など）。
// relevance retrieval に乗せると「今日のメッセージに当たらない回に自分の名前を忘れる」
// 事故が起きるため、UserProfile と同様に常時注入する固定枠として扱う。
type CharaProfile struct {
	CharacterID       string    `db:"character_id"`
	Name              string    `db:"name"`
	Age               *int      `db:"age"`
	Gender            string    `db:"gender"`
	Job               string    `db:"job"`
	RelationshipStory string    `db:"relationship_story"`
	UpdatedAt         time.Time `db:"updated_at"`
}

// GetCharaProfile はキャラクターのコア情報を取得する。
func (r *MemoryRepository) GetCharaProfile() (*CharaProfile, error) {
	var profile CharaProfile
	err := r.db.Get(&profile, `
		SELECT character_id, name, age, gender, job, relationship_story, updated_at
		FROM chara_profile
		WHERE character_id = $1`, r.CharacterID)
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

// UpsertCharaProfile はキャラクターのコア情報を保存・更新する。
// 空文字・nil は既存値を上書きしない（UserProfile と同じ方針）。
func (r *MemoryRepository) UpsertCharaProfile(name string, age *int, gender, job string) error {
	query := `
		INSERT INTO chara_profile (character_id, name, age, gender, job, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (character_id) DO UPDATE SET
			name       = COALESCE(NULLIF(EXCLUDED.name, ''),   chara_profile.name),
			age        = COALESCE(EXCLUDED.age,                 chara_profile.age),
			gender     = COALESCE(NULLIF(EXCLUDED.gender, ''), chara_profile.gender),
			job        = COALESCE(NULLIF(EXCLUDED.job, ''),    chara_profile.job),
			updated_at = NOW()`
	_, err := r.db.Exec(query, r.CharacterID, name, age, gender, job)
	return err
}

// UpdateCharaJob は会話中に検知した職業で chara_profile.job を更新する（シフト生成の源泉を最新に保つ）。
// 空文字は無視。行が無ければ no-op。
func (r *MemoryRepository) UpdateCharaJob(job string) error {
	if job == "" {
		return nil
	}
	_, err := r.db.Exec(`
		UPDATE chara_profile SET job = $1, updated_at = NOW() WHERE character_id = $2`,
		job, r.CharacterID)
	return err
}

// UpdateRelationshipStory はnightlyバッチからストーリーラインを更新する。
func (r *MemoryRepository) UpdateRelationshipStory(story string) error {
	_, err := r.db.Exec(`
		UPDATE chara_profile
		SET relationship_story = $1, updated_at = NOW()
		WHERE character_id = $2`,
		story, r.CharacterID)
	return err
}
