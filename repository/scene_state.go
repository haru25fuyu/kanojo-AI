package repository

import "time"

// SceneState はなりきりモードでの「今この2人がいる場所・状況」を保持する。
// per-(user, character) で1行。ターン中に現在地がブレないためのアンカー。
// 話題が動いたタイミングで返答から抽出し、上書きで更新する。
type SceneState struct {
	UserID      string    `db:"user_id"`
	CharacterID string    `db:"character_id"`
	Location    string    `db:"location"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// GetSceneState は現在地テキストを返す（無ければ空文字＋sql.ErrNoRows）。
func (r *MemoryRepository) GetSceneState() (string, error) {
	var loc string
	err := r.db.Get(&loc, `
		SELECT location FROM scene_state
		WHERE user_id = $1 AND character_id = $2`,
		r.UserID, r.CharacterID)
	return loc, err
}

// UpsertSceneLocation は現在地を上書き保存する（トリガー空振り時は同じ値で no-op）。
func (r *MemoryRepository) UpsertSceneLocation(location string) error {
	_, err := r.db.Exec(`
		INSERT INTO scene_state (user_id, character_id, location, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, character_id) DO UPDATE SET
			location   = EXCLUDED.location,
			updated_at = NOW()`,
		r.UserID, r.CharacterID, location)
	return err
}
