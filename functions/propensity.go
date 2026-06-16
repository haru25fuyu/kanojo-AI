package functions

import (
	"math"
	"sync"

	"go_app/gemini"
	"go_app/repository"
)

// ReplyMode はコード側で決定した応答モード
type ReplyMode int

const (
	ReplyNormal ReplyMode = iota // 通常返答（フル）
	ReplyShort                   // 短い返事（締め・軽い相づちにレジスタを合わせる時）
	ReplyDodge                   // 躱す（重い話を信頼が浅いうちは深入りせず受け流す。返事自体はする）
)

// PropensityInput は応答モード判定の入力
type PropensityInput struct {
	UserEmbedding []float64
	Status        *repository.PartnerStatus // dodge の trust ゲート用（nil 可）
}

// PropensityResult は判定結果
type PropensityResult struct {
	Mode    ReplyMode
	IsHeavy bool    // 重い話かどうか（外部での追加ロジック用）
	Score   float64 // デバッグ用（v2 では未使用・常に0）
	Reason  string  // デバッグ用理由
}

// DodgeInstruction は dodge モード時にメッセージへ追加する指示文
func DodgeInstruction() string {
	return "（この話題には自然にはぐらかして返答して。深入りせず、さらっと受け流すか話題を変えて。reply_type は \"normal\" にすること）"
}

// ShortInstruction は short モード時にメッセージへ追加する指示文。
// 「疲れているから」ではなく、締めや軽い相づちにレジスタを合わせて短くする意図。
func ShortInstruction() string {
	return "（「おやすみ」などの締めや軽い相づちには、長く返さず1〜2文で軽く返して。reply_type は \"short\" にすること）"
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
			if c := computeCentroid(seeds); c != nil {
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
		if v := gemini.GetEmbedding(t); len(v) > 0 {
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
// 判定本体（v2）
//   可用性ベースの摩擦（skip＝無返答 / 疲労による短文化）は廃止。AIは常に返す。
//   残すのは内容ベースの2つだけ。
// ─────────────────────────────────────────────────────────────────────────────

const (
	mandatoryThreshold  = 0.57 // 挨拶・質問・好意 → 必ず normal
	heavyThreshold      = 0.42 // これ以上で「重い話」とみなす
	lightThreshold      = 0.50 // 締め・軽い相づち → short
	dodgeTrustThreshold = 4000 // 重い話で、これ未満の信頼なら躱す
)

// ComputePropensity はユーザー入力の内容から応答モードを決める。
//   - 締め・軽い相づち（おやすみ等）→ short（レジスタを合わせて短く。疲れだからではない）
//   - 重い話 × 信頼が浅い          → dodge（深入りせず受け流す。返事自体はする）
//   - それ以外                     → normal
//
// skip（無返答）や status（疲労・気分）による短文化は行わない。
// クラスター未準備の場合は ReplyNormal にフォールバックする。
func ComputePropensity(p PropensityInput) PropensityResult {
	clusterMu.RLock()
	ready := clustersReady
	embs := clusterEmbs
	clusterMu.RUnlock()

	if !ready {
		return PropensityResult{Mode: ReplyNormal, Reason: "cluster_not_ready"}
	}

	heavySim := cosineSim(p.UserEmbedding, embs["heavy"])
	fillerSim := cosineSim(p.UserEmbedding, embs["filler"])
	closingSim := cosineSim(p.UserEmbedding, embs["closing"])
	greetingSim := cosineSim(p.UserEmbedding, embs["greeting"])
	questionSim := cosineSim(p.UserEmbedding, embs["question"])
	affectionSim := cosineSim(p.UserEmbedding, embs["affection"])

	// 挨拶・質問・好意/お礼/謝罪 → 必ず normal（ちゃんと向き合う）
	if greetingSim > mandatoryThreshold ||
		questionSim > mandatoryThreshold ||
		affectionSim > mandatoryThreshold {
		return PropensityResult{Mode: ReplyNormal, Reason: "mandatory_guard"}
	}

	// 重い話 → 信頼が浅ければ躱す、深ければ正面から受け止める
	if heavySim > heavyThreshold {
		if p.Status != nil && p.Status.Trust >= dodgeTrustThreshold {
			return PropensityResult{Mode: ReplyNormal, IsHeavy: true, Reason: "heavy_high_trust"}
		}
		return PropensityResult{Mode: ReplyDodge, IsHeavy: true, Reason: "heavy_low_trust"}
	}

	// 締め・軽い相づち → レジスタを合わせて短く
	if closingSim > lightThreshold || fillerSim > lightThreshold {
		return PropensityResult{Mode: ReplyShort, Reason: "light_register"}
	}

	return PropensityResult{Mode: ReplyNormal, Reason: "normal"}
}
