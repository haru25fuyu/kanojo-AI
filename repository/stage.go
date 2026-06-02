package repository

type CharacterStage struct {
	CharacterID string  `db:"character_id"`
	Parameter   string  `db:"parameter"`
	StageFrom   int     `db:"stage_from"`
	StageTo     int     `db:"stage_to"`
	Prompt      string  `db:"prompt"`
	SortOrder   int     `db:"sort_order"`
	FilterParam *string `db:"filter_param"`
	FilterFrom  *int    `db:"filter_from"`
	FilterTo    *int    `db:"filter_to"`
}

func (r *MemoryRepository) GetCharacterStages(characterID string) ([]CharacterStage, error) {
	var stages []CharacterStage
	err := r.db.Select(&stages, `
        SELECT character_id, parameter, stage_from, stage_to,
               prompt, sort_order, filter_param, filter_from, filter_to
        FROM character_stages
        WHERE character_id = $1
        ORDER BY parameter, sort_order
    `, characterID)
	return stages, err
}
