package functions

import (
	"math"
	"math/rand"
	"sync"

	"go_app/gemini"
	"go_app/repository"
)

// ReplyMode はコード側で決定した応答モード
type ReplyMode int

const (
	ReplyNormal ReplyMode = iota // 通常返答
	ReplyShort                   // 短い返事（疲れ・低活力）
	ReplyDodge                   // 躱す（返答するが深入りしない）
)

// PropensityInput は応答モード判定の入力
type PropensityInput struct {
	UserEmbedding []float64
	Status        *repository.PartnerStatus
}

// PropensityResult は判定結果
type PropensityResult struct {
	Mode    ReplyMode
	IsHeavy bool    // 重い話かどうか（外部での追加ロジック用）
	Score   float64 // デバッグ用スコア
	Reason  string  // デバッグ用理由
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

const (
	threshNormal = 12.0 // score >= threshNormal → normal
	threshShort  = -5.0 // score >= threshShort  → short
	// score < threshShort → dodge（skip は廃止。AIは必ず返す）
)

const mandatoryThreshold = 0.57

// ComputePropensity はユーザー入力の埋め込みと各種コンテキストから応答モードを決定する。
// skip（無返答）は廃止。クラスターが未準備の場合は ReplyNormal にフォールバックする。
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
	greetingSim := cosineSim(p.UserEmbedding, embs["greeting"])
	questionSim := cosineSim(p.UserEmbedding, embs["question"])
	affectionSim := cosineSim(p.UserEmbedding, embs["affection"])

	// ── 義務ガード：挨拶・質問・好意/お礼/謝罪 → 必ず normal で返す ────────
	if greetingSim > mandatoryThreshold ||
		questionSim > mandatoryThreshold ||
		affectionSim > mandatoryThreshold {
		return PropensityResult{Mode: ReplyNormal, Reason: "mandatory_guard"}
	}

	// ── プロペンシティスコア ───────────────────────────────────────────────
	var score float64

	score -= heavySim * 35
	score -= fillerSim * 20

	if s := p.Status; s != nil {
		score += float64(s.Mood) / 800
		score -= float64(s.Stress) / 1000
		score -= float64(s.Fatigue) / 1200
		score += float64(s.Energy) / 1200
		score += float64(s.Affection) / 800
	}

	noise := (rand.Float64() - 0.5) * 10
	finalScore := score + noise
	isHeavy := heavySim > 0.42

	// ── 帯分け（skip なし。最低でも dodge で返す）─────────────────────────
	var mode ReplyMode
	var reason string

	switch {
	case finalScore >= threshNormal:
		mode = ReplyNormal
		reason = "score_normal"

	case finalScore >= threshShort:
		mode = ReplyShort
		reason = "score_short"

	default:
		if isHeavy {
			if p.Status != nil && p.Status.Trust >= 4000 {
				mode = ReplyNormal
				reason = "heavy_high_trust"
			} else {
				mode = ReplyDodge
				reason = "heavy_low_trust"
			}
		} else {
			mode = ReplyDodge
			reason = "score_dodge"
		}
	}

	return PropensityResult{Mode: mode, IsHeavy: isHeavy, Score: finalScore, Reason: reason}
}
