package repository

import "time"

// InnerState はキャラクターの現在の気分テキストを保持する。
// 周期 + 変化率トリガーで更新され、LLM プロンプトの statusText として差し込む。
type InnerState struct {
	CharacterID string    `db:"character_id"`
	MoodText    string    `db:"mood_text"`     // 地＋表層を合わせた気分テキスト
	MoodAtGen   int       `db:"mood_at_gen"`   // 生成時点の気分値（変化検知用）
	StressAtGen int       `db:"stress_at_gen"` // 生成時点のストレス値（変化検知用）
	NextRunAt   time.Time `db:"next_run_at"`   // 次の定期生成予定時刻
	UpdatedAt   time.Time `db:"updated_at"`
}

// GetInnerState は現在の内面状態を取得する。
func (r *MemoryRepository) GetInnerState() (*InnerState, error) {
	var s InnerState
	err := r.db.Get(&s, `
		SELECT character_id, mood_text, mood_at_gen, stress_at_gen, next_run_at, updated_at
		FROM inner_state
		WHERE character_id = $1`, r.CharacterID)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveMoodState は気分テキストと次回実行時刻を全項目 upsert する（タイマー発火後に呼ぶ）。
func (r *MemoryRepository) SaveMoodState(moodText string, moodAtGen, stressAtGen int, nextRunAt time.Time) error {
	query := `
		INSERT INTO inner_state (character_id, mood_text, mood_at_gen, stress_at_gen, next_run_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (character_id) DO UPDATE SET
			mood_text     = EXCLUDED.mood_text,
			mood_at_gen   = EXCLUDED.mood_at_gen,
			stress_at_gen = EXCLUDED.stress_at_gen,
			next_run_at   = EXCLUDED.next_run_at,
			updated_at    = NOW()`
	_, err := r.db.Exec(query, r.CharacterID, moodText, moodAtGen, stressAtGen, nextRunAt)
	return err
}

// SaveMoodOnly は気分テキストのみ更新する（変化率トリガー後に呼ぶ。next_run_at は変えない）。
func (r *MemoryRepository) SaveMoodOnly(moodText string, moodAtGen, stressAtGen int) error {
	query := `
		UPDATE inner_state SET
			mood_text     = $2,
			mood_at_gen   = $3,
			stress_at_gen = $4,
			updated_at    = NOW()
		WHERE character_id = $1`
	_, err := r.db.Exec(query, r.CharacterID, moodText, moodAtGen, stressAtGen)
	return err
}
