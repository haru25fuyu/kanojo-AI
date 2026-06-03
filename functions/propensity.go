package functions

import (
	"math"
	"math/rand"
	"strings"
	"sync"

	"go_app/gemini"
	"go_app/repository"
)

// ReplyMode はコード側で決定した応答モード
type ReplyMode int

const (
	ReplyNormal ReplyMode = iota // 通常返答
	ReplyShort                   // 短い返事（疲れ・低活力）
	ReplySkip                    // 無返答（絵文字のみ・LLM 不使用）
	ReplyDodge                   // 躱す（返答するが深入りしない）
)

// PropensityInput は応答モード判定の入力
type PropensityInput struct {
	UserEmbedding     []float64
	LastUserEmbedding []float64 // 反復検出用（nil 可）
	LastAIContent     string    // 締めの skip 判定用（空文字可）
	Status            *repository.PartnerStatus
}

// PropensityResult は判定結果
type PropensityResult struct {
	Mode    ReplyMode
	IsHeavy bool    // 重い話かどうか（外部での追加ロジック用）
	Score   float64 // デバッグ用スコア
	Reason  string  // デバッグ用理由
}

// SkipEmoji は skip 時に付ける絵文字を返す
func SkipEmoji(r PropensityResult, status *repository.PartnerStatus) string {
	if r.Reason == "mutual_closing" {
		return "🌙"
	}
	if status != nil {
		switch {
		case status.Fatigue >= 7000:
			return "😴"
		case status.Affection >= 6000:
			return "❤️" // 好きだけど今は無理
		}
	}
	return "💭"
}

// DodgeInstruction は dodge モード時にメッセージへ追加する指示文
func DodgeInstruction() string {
	return "（この話題には自然にはぐらかして返答して。深入りせず、さらっと受け流すか話題を変えて。reply_type は \"normal\" にすること）"
}

// ShortInstruction は short モード時にメッセージへ追加する指示文
func ShortInstruction() string {
	return "（今は疲れてるか気分が乗らない。1〜2文の短い返事だけでいい。reply_type は \"short\" にすること）"
}

// ─────────────────────────────────────────────────────────────────────────────
// クラスター埋め込み
// ─────────────────────────────────────────────────────────────────────────────

// clusterSeeds は各クラスターのシードフレーズ
var clusterSeeds = map[string][]string{
	"heavy": {
		"死にたい、消えたい、もうだめだ",
		"悲しくてつらい、苦しい",
		"誰にも言えなかったんだけど、ひどいことがあった",
	},
	"filler": {
		"笑笑",
		"そっかー、ふーん",
		"あ、なるほどね",
	},
	"greeting": {
		"おはよう！",
		"こんにちは、元気？",
		"久しぶり、やっほー",
	},
	"closing": {
		"おやすみなさい",
		"またね、じゃあね",
		"バイバイ、またあした",
	},
	"question": {
		"どう思う？教えて",
		"なんで？どういうこと？",
		"これってどうすればいいかな？",
	},
	"affection": {
		"ありがとう、大好き",
		"ごめんなさい、ごめんね",
		"好きだよ、嬉しかった",
	},
}

var (
	clusterEmbs   map[string][]float64
	clusterMu     sync.RWMutex
	clustersReady bool
)

// InitClusters はクラスター重心埋め込みを非同期で計算する。
// main() の起動直後に呼ぶこと。計算中は ComputePropensity が ReplyNormal にフォールバックする。
func InitClusters() {
	go func() {
		embs := make(map[string][]float64)
		for name, seeds := range clusterSeeds {
			c := computeCentroid(seeds)
			if c != nil {
				embs[name] = c
			}
		}
		clusterMu.Lock()
		clusterEmbs = embs
		clustersReady = true
		clusterMu.Unlock()
	}()
}

func computeCentroid(texts []string) []float64 {
	var vecs [][]float64
	for _, t := range texts {
		v := gemini.GetEmbedding(t)
		if len(v) > 0 {
			vecs = append(vecs, v)
		}
	}
	if len(vecs) == 0 {
		return nil
	}
	dim := len(vecs[0])
	centroid := make([]float64, dim)
	for _, v := range vecs {
		for i, x := range v {
			centroid[i] += x
		}
	}
	for i := range centroid {
		centroid[i] /= float64(len(vecs))
	}
	return centroid
}

func cosineSim(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ─────────────────────────────────────────────────────────────────────────────
// 判定本体
// ─────────────────────────────────────────────────────────────────────────────

// スコア帯の境界値（SQL の settings で上書き可能にする余地があるが、まず固定で運用）
const (
	threshNormal = 12.0  // score >= threshNormal → normal
	threshShort  = -5.0  // score >= threshShort  → short
	threshSkip   = -25.0 // score >= threshSkip   → skip/dodge
	// score < threshSkip → deep skip/dodge
)

// 義務ガードの閾値
const mandatoryThreshold = 0.57

// ComputePropensity はユーザー入力の埋め込みと各種コンテキストから応答モードを決定する。
// クラスターが未準備の場合は ReplyNormal にフォールバックする。
func ComputePropensity(p PropensityInput) PropensityResult {
	clusterMu.RLock()
	ready := clustersReady
	embs := clusterEmbs
	clusterMu.RUnlock()

	if !ready {
		return PropensityResult{Mode: ReplyNormal, Reason: "cluster_not_ready"}
	}

	// ── 各クラスターとの類似度 ─────────────────────────────────────────────
	heavySim := cosineSim(p.UserEmbedding, embs["heavy"])
	fillerSim := cosineSim(p.UserEmbedding, embs["filler"])
	closingSim := cosineSim(p.UserEmbedding, embs["closing"])
	greetingSim := cosineSim(p.UserEmbedding, embs["greeting"])
	questionSim := cosineSim(p.UserEmbedding, embs["question"])
	affectionSim := cosineSim(p.UserEmbedding, embs["affection"])

	// ── 義務ガード：挨拶・質問・好意/お礼/謝罪 → 必ず normal で返す ────────
	if greetingSim > mandatoryThreshold ||
		questionSim > mandatoryThreshold ||
		affectionSim > mandatoryThreshold {
		return PropensityResult{Mode: ReplyNormal, Reason: "mandatory_guard"}
	}

	// ── 締めの相互 skip ─────────────────────────────────────────────────────
	// 今の発言が「締め」 AND 直前のAI発言も「締め」内容 → skip（お互いおやすみ完了）
	if closingSim > 0.55 && p.LastAIContent != "" && isClosingContent(p.LastAIContent) {
		return PropensityResult{Mode: ReplySkip, Reason: "mutual_closing", Score: -100}
	}

	// ── プロペンシティスコア ───────────────────────────────────────────────
	var score float64

	// コンテンツ成分
	score -= heavySim * 35
	score -= fillerSim * 20

	// status 成分（各値の意味：mood は -10000〜10000、他は 0〜10000）
	if s := p.Status; s != nil {
		score += float64(s.Mood) / 800      // 気分が良いほど +
		score -= float64(s.Stress) / 1000   // ストレスが高いほど -
		score -= float64(s.Fatigue) / 1200  // 疲労が高いほど -
		score += float64(s.Energy) / 1200   // 活力が高いほど +
		score += float64(s.Affection) / 800 // 好きだから返したい（逆相関で skip しにくい）
	}

	// 反復ペナルティ：直前と同じ内容を繰り返してる → skip 寄りに
	if len(p.LastUserEmbedding) > 0 {
		repSim := cosineSim(p.UserEmbedding, p.LastUserEmbedding)
		switch {
		case repSim > 0.88:
			score -= 28 // ほぼ同一内容
		case repSim > 0.75:
			score -= 14
		}
	}

	// 確率ノイズ（±5）：毎回同じ判定にならず、"たまにそっけない" が自然に出る
	noise := (rand.Float64() - 0.5) * 10
	finalScore := score + noise
	isHeavy := heavySim > 0.42

	// ── 帯分け ─────────────────────────────────────────────────────────────
	var mode ReplyMode
	var reason string

	switch {
	case finalScore >= threshNormal:
		mode = ReplyNormal
		reason = "score_normal"

	case finalScore >= threshShort:
		mode = ReplyShort
		reason = "score_short"

	case finalScore >= threshSkip:
		if isHeavy {
			if p.Status != nil && p.Status.Trust >= 4000 {
				// 重い話 × 高 trust → 正面から受け止める
				mode = ReplyNormal
				reason = "heavy_high_trust"
			} else {
				// 重い話 × 低 trust → 躱す（絶対 skip にしない）
				mode = ReplyDodge
				reason = "heavy_low_trust"
			}
		} else {
			mode = ReplySkip
			reason = "score_skip"
		}

	default: // score < threshSkip（かなり悪いコンディション）
		if isHeavy {
			mode = ReplyDodge // 重い話はどんな状況でも skip にしない
			reason = "heavy_forced_dodge"
		} else {
			mode = ReplySkip
			reason = "score_deep_skip"
		}
	}

	return PropensityResult{Mode: mode, IsHeavy: isHeavy, Score: finalScore, Reason: reason}
}

// isClosingContent は文字列が締め・別れの内容かをキーワードで判定する（lastAIContent 用）
func isClosingContent(text string) bool {
	keywords := []string{
		"おやすみ", "またね", "じゃあね", "バイバイ",
		"行ってきます", "またあした", "またあとで", "お休み",
	}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}
