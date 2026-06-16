package repository

import (
	"context"
	"log"
	"strings"
	"time"

	"go_app/gemini"

	"github.com/lib/pq"
)

// SceneStory はなりきりの「2人の物語」を per-(user, character) で保持する。
//   - summary      = 畳み込み済みのあらすじ（過去〜昨日までの筋。夜バッチで更新）
//   - recent_beats = まだ畳まれていない会話単位の物語要約（日中に追記）
//
// 注入時は summary + recent_beats を1本にして「これまでの物語」として渡す。
// 現在地（scene_state）が"今この瞬間"のアンカーなのに対し、こちらは会話をまたいだ"弧"を持つ。
type SceneStory struct {
	UserID      string         `db:"user_id"`
	CharacterID string         `db:"character_id"`
	Summary     string         `db:"summary"`
	RecentBeats pq.StringArray `db:"recent_beats"`
	UpdatedAt   time.Time      `db:"updated_at"`
}

// GetSceneStory はあらすじと直近ビート群を返す。
func (r *MemoryRepository) GetSceneStory() (string, []string, error) {
	var s SceneStory
	err := r.db.Get(&s, `
		SELECT user_id, character_id, summary, recent_beats, updated_at
		FROM scene_story
		WHERE user_id = $1 AND character_id = $2`,
		r.UserID, r.CharacterID)
	if err != nil {
		return "", nil, err
	}
	return s.Summary, []string(s.RecentBeats), nil
}

// GetSceneStoryText は注入用に、あらすじ＋直近ビートを1本のテキストにして返す。
func (r *MemoryRepository) GetSceneStoryText() (string, error) {
	summary, beats, err := r.GetSceneStory()
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(beats)+1)
	if summary != "" {
		parts = append(parts, summary)
	}
	parts = append(parts, beats...)
	return strings.Join(parts, "\n"), nil
}

// AppendSceneBeat は会話単位の物語要約をバッファ末尾に追加する（日中の追記。畳み込みはしない）。
func (r *MemoryRepository) AppendSceneBeat(beat string) error {
	_, err := r.db.Exec(`
		INSERT INTO scene_story (user_id, character_id, recent_beats, updated_at)
		VALUES ($1, $2, ARRAY[$3], NOW())
		ON CONFLICT (user_id, character_id) DO UPDATE SET
			recent_beats = array_append(scene_story.recent_beats, $3),
			updated_at   = NOW()`,
		r.UserID, r.CharacterID, beat)
	return err
}

// SaveSceneFold は夜の畳み込み結果（新しいあらすじ）を保存し、バッファをクリアする。
func (r *MemoryRepository) SaveSceneFold(summary string) error {
	_, err := r.db.Exec(`
		INSERT INTO scene_story (user_id, character_id, summary, recent_beats, updated_at)
		VALUES ($1, $2, $3, '{}', NOW())
		ON CONFLICT (user_id, character_id) DO UPDATE SET
			summary      = EXCLUDED.summary,
			recent_beats = '{}',
			updated_at   = NOW()`,
		r.UserID, r.CharacterID, summary)
	return err
}

// FoldSceneStoryNightly はその日に溜まった物語ビートをあらすじへ畳み込む。
// 夜バッチの per-(user, character) ループから呼ぶ。ビートが無ければ何もしない。
// 切れ目が無人時間帯に隠れ、劣化が人の一晩の忘却と揃う。
func (r *MemoryRepository) FoldSceneStoryNightly(ctx context.Context, modelBatch string) {
	summary, beats, err := r.GetSceneStory()
	if err != nil || len(beats) == 0 {
		return
	}
	folded, err := gemini.FoldSceneStory(ctx, modelBatch, summary, beats)
	if err != nil || folded == "" {
		return
	}
	if err := r.SaveSceneFold(folded); err != nil {
		log.Printf("物語畳み込み保存失敗: %v", err)
		return
	}
	log.Printf("物語畳み込み完了 user=%s chara=%s", r.UserID, r.CharacterID)
}
