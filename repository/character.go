package repository

import "time"

type Character struct {
	ID               string    `db:"id"`
	Name             string    `db:"name"`
	SystemPrompt     string    `db:"system_prompt"`
	ProactiveChannel string    `db:"proactive_channel"`
	Active           bool      `db:"active"`
	CharaInfoSeeded  bool      `db:"chara_info_seeded"`
	CreatedAt        time.Time `db:"created_at"`
}

// GetCharacter はIDでキャラクターを取得する
func (r *MemoryRepository) GetCharacter(id string) (*Character, error) {
	var c Character
	err := r.db.Get(&c, `SELECT id, name, system_prompt, proactive_channel, active, created_at FROM characters WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetActiveCharacters はアクティブな全キャラクターを取得する
func (r *MemoryRepository) GetActiveCharacters() ([]Character, error) {
	var chars []Character
	err := r.db.Select(&chars, `SELECT id, name, system_prompt, proactive_channel, active, created_at FROM characters WHERE active = TRUE`)
	return chars, err
}
