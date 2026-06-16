package repository

// GetRulePrompt はモードに対応する出力ルール（レジスタ）を返す。
//   - chat（既定）      → 既存の system_prompt_rule をそのまま使う
//   - roleplay          → system_prompt_rule_roleplay
//   - knowledge         → system_prompt_rule_knowledge
//
// 専用プリセットが未設定なら system_prompt_rule にフォールバックする。
//
// 現状 mode はグローバル設定 chat_mode を渡す想定。将来キャラ単位にしたくなったら、
// 呼び出し側で chara.Mode を渡すだけでよく、この関数は変更不要。
func (r *MemoryRepository) GetRulePrompt(mode string) string {
	switch mode {
	case "roleplay":
		if rule := r.GetSetting("system_prompt_rule_roleplay", ""); rule != "" {
			return rule
		}
	case "knowledge":
		if rule := r.GetSetting("system_prompt_rule_knowledge", ""); rule != "" {
			return rule
		}
	}
	return r.GetSetting("system_prompt_rule", "日常会話に徹してください。")
}
