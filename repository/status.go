package repository

import (
	"log"
	"time"
)

type PartnerStatus struct {
	Affection int       `db:"affection"` // 好感度 0〜100
	Trust     int       `db:"trust"`     // 信頼度 0〜100
	Fatigue   int       `db:"fatigue"`   // 疲労度 0〜100
	Mood      int       `db:"mood"`      // 気分 -100〜100
	Stress    int       `db:"stress"`    // ストレス 0〜100
	Energy    int       `db:"energy"`    // 活力 0〜100
	UpdatedAt time.Time `db:"updated_at"`
}

type StatusDelta struct {
	Affection int `json:"affection"`
	Trust     int `json:"trust"`
	Fatigue   int `json:"fatigue"`
	Mood      int `json:"mood"`
	Stress    int `json:"stress"`
	Energy    int `json:"energy"`
}

// GetPartnerStatus は現在のパートナーステータスを取得する（なければ自動作成）
func (r *MemoryRepository) GetPartnerStatus() (*PartnerStatus, error) {
	log.Printf("GetPartnerStatus: user=%s chara=%s", r.UserID, r.CharacterID)
	// なければ初期値でINSERT
	r.db.Exec(`
		INSERT INTO partner_status (user_id, character_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, r.UserID, r.CharacterID)

	var status PartnerStatus
	err := r.db.Get(&status, `SELECT affection, trust, fatigue, mood, stress, energy, updated_at FROM partner_status WHERE user_id = $1 AND character_id = $2`, r.UserID, r.CharacterID)
	if err != nil {
		return nil, err
	}
	return &status, nil
}

// ApplyStatusDelta はLLMが返した増減値をステータスに適用する（0〜100にクランプ）
func (r *MemoryRepository) ApplyStatusDelta(delta StatusDelta) error {
	query := `
		UPDATE partner_status SET
			affection  = GREATEST(0, LEAST(10000,  affection  + $1)),
			trust      = GREATEST(0, LEAST(10000,  trust      + $2)),
			fatigue    = GREATEST(0, LEAST(10000,  fatigue    + $3)),
			mood       = GREATEST(-10000, LEAST(10000, mood  + $4)),
			stress     = GREATEST(0, LEAST(10000,  stress     + $5)),
			energy     = GREATEST(0, LEAST(10000,  energy     + $6)),
			updated_at = NOW()
		WHERE user_id = $7 AND character_id = $8`
	_, err := r.db.Exec(query, delta.Affection, delta.Trust, delta.Fatigue, delta.Mood, delta.Stress, delta.Energy, r.UserID, r.CharacterID)
	return err
}
