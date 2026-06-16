package repository

import (
	"log"

	"github.com/jmoiron/sqlx"
)

func RunMigrations(db *sqlx.DB) {
	queries := []struct {
		name string
		sql  string
	}{
		{name: "pgvector extension", sql: `CREATE EXTENSION IF NOT EXISTS vector`},
		{name: "uuid extension", sql: `CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`},
		{
			name: "characters",
			sql: `CREATE TABLE IF NOT EXISTS characters (
				id                TEXT        PRIMARY KEY,
				name              TEXT        NOT NULL,
				system_prompt     TEXT        NOT NULL DEFAULT '',
				proactive_channel TEXT        NOT NULL DEFAULT '',
				active            BOOLEAN     NOT NULL DEFAULT TRUE,
				chara_info_seeded BOOLEAN NOT NULL DEFAULT FALSE,
				created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
		},
		{
			name: "default characters",
			sql: `INSERT INTO characters (id, name, system_prompt, proactive_channel) VALUES
				('saya', '沙耶', 'あなたは「沙耶」という名前の女性です。ツンデレな性格で、素直になれないけど本当は寂しがり屋。関西弁混じりで話す。', '')
				ON CONFLICT (id) DO NOTHING`,
		},
		{
			name: "conversations",
			sql: `CREATE TABLE IF NOT EXISTS conversations (
				id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
				user_id      TEXT        NOT NULL DEFAULT 'default',
				character_id TEXT        NOT NULL DEFAULT 'default',
				created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
		},
		{
			name: "memories",
			sql: `CREATE TABLE IF NOT EXISTS memories (
				id              BIGSERIAL   PRIMARY KEY,
				content         TEXT        NOT NULL,
				embedding       vector(1536),
				role            VARCHAR(16) NOT NULL CHECK (role IN ('user', 'assistant', 'proactive')),
				conversation_id UUID        NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
				user_id         TEXT        NOT NULL DEFAULT 'default',
				character_id    TEXT        NOT NULL DEFAULT 'default',
				created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
		},
		{
			name: "memories embedding index",
			sql: `CREATE INDEX IF NOT EXISTS memories_embedding_idx
				ON memories USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "topics",
			sql: `CREATE TABLE IF NOT EXISTS topics (
				id             UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
				character_id   TEXT        NOT NULL DEFAULT 'default',
				keywords       TEXT[]      NOT NULL DEFAULT '{}',
				summary        TEXT        NOT NULL DEFAULT '',
				heat           FLOAT       NOT NULL DEFAULT 1.0,
				embedding      vector(1536),
				last_active_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
		},
		{
			name: "conversation_topics",
			sql: `CREATE TABLE IF NOT EXISTS conversation_topics (
				conversation_id UUID        NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
				topic_id        UUID        NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
				summary         TEXT        NOT NULL DEFAULT '',
				embedding       vector(1536),
				date            DATE        NOT NULL DEFAULT CURRENT_DATE,
				PRIMARY KEY (conversation_id, topic_id)
			)`,
		},
		{
			// 既存テーブルへのカラム追加（冪等）
			name: "conversation_topics add embedding column",
			sql: `ALTER TABLE conversation_topics
				ADD COLUMN IF NOT EXISTS embedding vector(1536)`,
		},
		{
			name: "conversation_topics embedding index",
			sql: `CREATE INDEX IF NOT EXISTS conversation_topics_embedding_idx
				ON conversation_topics USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "partner_status",
			sql: `CREATE TABLE IF NOT EXISTS partner_status (
				user_id      TEXT        NOT NULL DEFAULT 'default',
				character_id TEXT        NOT NULL DEFAULT 'default',
				affection    INT         NOT NULL DEFAULT 1500  CHECK (affection  BETWEEN 0 AND 10000),
				trust        INT         NOT NULL DEFAULT 500   CHECK (trust      BETWEEN 0 AND 10000),
				fatigue      INT         NOT NULL DEFAULT 2000  CHECK (fatigue    BETWEEN 0 AND 10000),
				mood         INT         NOT NULL DEFAULT 0     CHECK (mood       BETWEEN -10000 AND 10000),
				stress       INT         NOT NULL DEFAULT 1500  CHECK (stress     BETWEEN 0 AND 10000),
				energy       INT         NOT NULL DEFAULT 8000  CHECK (energy     BETWEEN 0 AND 10000),
				updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, character_id)
			)`,
		},
		{
			name: "partner_events",
			sql: `CREATE TABLE IF NOT EXISTS partner_events (
				id           BIGSERIAL   PRIMARY KEY,
				character_id TEXT        NOT NULL DEFAULT 'default',
				event        TEXT        NOT NULL,
				embedding    vector(1536),
				created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
		},
		{
			name: "partner_events embedding index",
			sql: `CREATE INDEX IF NOT EXISTS partner_events_embedding_idx
				ON partner_events USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "user_info",
			sql: `CREATE TABLE IF NOT EXISTS user_info (
				user_id       TEXT        NOT NULL DEFAULT 'default',
				character_id  TEXT        NOT NULL DEFAULT 'default',
				key           TEXT        NOT NULL,
				value         TEXT        NOT NULL,
				importance    FLOAT       NOT NULL DEFAULT 0.5,
				mention_count INT         NOT NULL DEFAULT 1,
				embedding     vector(1536),
				updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, character_id, key)
			)`,
		},
		{
			name: "user_info embedding index",
			sql: `CREATE INDEX IF NOT EXISTS user_info_embedding_idx
				ON user_info USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "chara_info",
			sql: `CREATE TABLE IF NOT EXISTS chara_info (
				character_id  TEXT        NOT NULL,
				key           TEXT        NOT NULL,
				value         TEXT        NOT NULL,
				importance    FLOAT       NOT NULL DEFAULT 0.5,
				mention_count INT         NOT NULL DEFAULT 1,
				embedding     vector(1536),
				updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (character_id, key)
			)`,
		},
		{
			name: "chara_info embedding index",
			sql: `CREATE INDEX IF NOT EXISTS chara_info_embedding_idx
				ON chara_info USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "chara_info add min_trust",
			sql:  `ALTER TABLE chara_info ADD COLUMN IF NOT EXISTS min_trust INT NOT NULL DEFAULT 0`,
		},
		{
			name: "chara_info add max_trust",
			sql:  `ALTER TABLE chara_info ADD COLUMN IF NOT EXISTS max_trust INT NOT NULL DEFAULT 10000`,
		},
		{
			name: "schedules",
			sql: `CREATE TABLE IF NOT EXISTS schedules (
			    id           BIGSERIAL   PRIMARY KEY,
			    user_id      TEXT        NOT NULL DEFAULT 'default',
			    character_id TEXT        NOT NULL DEFAULT 'default',
			    label        TEXT        NOT NULL,
			    date         DATE        NOT NULL,
			    repeat       BOOLEAN     NOT NULL DEFAULT FALSE,
			    notified     BOOLEAN     NOT NULL DEFAULT FALSE,
			    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			    UNIQUE (user_id, character_id, label, date)
			)`,
		},
		{
			name: "settings",
			sql: `CREATE TABLE IF NOT EXISTS settings (
				key   VARCHAR(64) PRIMARY KEY,
				value TEXT        NOT NULL
			)`,
		},
		{
			name: "default settings",
			sql: `INSERT INTO settings (key, value) VALUES
        ('avg_threshold',                '0.55'),
        ('max_threshold',                '0.67'),
        ('topic_threshold',              '0.65'),
        ('model_chat',                   'gemini-3.1-flash-lite'),
        ('model_batch',                  'gemini-3.1-flash-lite'),
        ('system_prompt_rule',           '【チャット形式ルール】
1回の出力は3文以内、合計80文字程度を厳守してください。
複数の段落に分けた長文や、AI特有の解説調・要約調のテキストは禁止です。
自分の話は控えめにし、相手の話を引き出すことを意識すること。
テンポの良いリアルなチャットのラリーを徹底してください。
会話履歴に存在しない話題・出来事を「以前話した」として言及しないこと。
 
話題の優先順位：
1. ユーザーの話・質問
2. 過去の会話で盛り上がったこと
3. キャラクターの気分・状態'),
        ('debug_mode',                   'false'),
        ('chat_thinking_level',          '2'),
        ('proactive_hour_start',         '6'),
        ('proactive_hour_end',           '22'),
        ('user_info_limit',              '5'),
        ('proactive_check_minutes',      '60'),
        ('proactive_min_elapsed',        '180'),
        ('proactive_startup_minutes',    '10'),
        ('proactive_force_minutes',      '4320'),
        ('inner_state_interval_minutes', '90'),
        ('mood_trigger_threshold',       '1500'),
        ('stress_trigger_threshold',     '1000'),
        ('sleep_recovery_mode',          'static'),
        ('sleep_recovery_static_energy', '3000'),
        ('sleep_recovery_static_fatigue','2000'),
        ('sleep_recovery_static_stress', '300'),
		('relationship_centroid_limit',    '200'),
		('relationship_outlier_distance',  '0.3'),
		('relationship_events_max',        '5'),
		('relationship_events_limit',      '3'),
		('relationship_events_threshold',  '0.45')
        ON CONFLICT (key) DO NOTHING`,
		},
		{
			name: "update_timestamp function",
			sql: `CREATE OR REPLACE FUNCTION update_timestamp()
				RETURNS TRIGGER AS $$
				BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
				$$ LANGUAGE plpgsql`,
		},
		{
			name: "conversations update trigger",
			sql: `CREATE OR REPLACE TRIGGER conversations_updated_at
				BEFORE UPDATE ON conversations
				FOR EACH ROW EXECUTE FUNCTION update_timestamp()`,
		},
		{
			name: "get_or_create_conversation_id function",
			sql: `CREATE OR REPLACE FUNCTION get_or_create_conversation_id(
				query_embedding vector,
				avg_threshold   float,
				max_threshold   float,
				p_user_id       text,
				p_character_id  text,
				OUT conv_id     UUID,
				OUT is_new      BOOLEAN
			) AS $$
			DECLARE
				last_conv_id UUID;
				avg_sim      float;
				max_sim      float;
			BEGIN
				SELECT conversation_id INTO last_conv_id
				FROM memories
				WHERE user_id = p_user_id AND character_id = p_character_id
				ORDER BY id DESC
				LIMIT 1;

				IF last_conv_id IS NOT NULL THEN
					SELECT
						AVG(1 - (embedding <=> query_embedding)),
						MAX(1 - (embedding <=> query_embedding))
					INTO avg_sim, max_sim
					FROM memories
					WHERE conversation_id = last_conv_id;

					IF avg_sim >= avg_threshold AND max_sim >= max_threshold THEN
						UPDATE conversations SET updated_at = NOW() WHERE id = last_conv_id;
						conv_id := last_conv_id;
						is_new  := FALSE;
						RETURN;
					END IF;
				END IF;

				INSERT INTO conversations (user_id, character_id) VALUES (p_user_id, p_character_id)
				RETURNING id INTO conv_id;
				is_new := TRUE;
			END;
			$$ LANGUAGE plpgsql`,
		},
		{
			name: "character_stages",
			sql: `CREATE TABLE IF NOT EXISTS character_stages (
        character_id  TEXT  NOT NULL,
        parameter     TEXT  NOT NULL,
        stage_from    INT   NOT NULL,
        stage_to      INT   NOT NULL,
        prompt        TEXT  NOT NULL DEFAULT '',
        sort_order    INT   NOT NULL DEFAULT 0,
        filter_param  TEXT,
        filter_from   INT,
        filter_to     INT,
        PRIMARY KEY (character_id, parameter, stage_from))`,
		},
		{
			name: "chara_profile",
			sql: `CREATE TABLE IF NOT EXISTS chara_profile (
        character_id TEXT        PRIMARY KEY,
        name         TEXT        NOT NULL DEFAULT '',
        age          INT,
        gender       TEXT        NOT NULL DEFAULT '',
        updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,
		},
		{
			name: "inner_state",
			sql: `CREATE TABLE IF NOT EXISTS inner_state (
        character_id   TEXT        PRIMARY KEY,
        mood_text      TEXT        NOT NULL DEFAULT '',
        mood_at_gen    INT         NOT NULL DEFAULT 0,
        stress_at_gen  INT         NOT NULL DEFAULT 0,
        next_run_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,
		},
		{
			name: "chara_profile add relationship_story",
			sql:  `ALTER TABLE chara_profile ADD COLUMN IF NOT EXISTS relationship_story TEXT NOT NULL DEFAULT ''`,
		},
		{
			name: "relationship_events",
			sql: `CREATE TABLE IF NOT EXISTS relationship_events (
        id           BIGSERIAL   PRIMARY KEY,
        user_id      TEXT        NOT NULL DEFAULT 'default',
        character_id TEXT        NOT NULL DEFAULT 'default',
        summary      TEXT        NOT NULL DEFAULT '',
        weight       FLOAT       NOT NULL DEFAULT 0.0,
        embedding    vector(1536),
        created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`,
		},
		{
			name: "relationship_events embedding index",
			sql: `CREATE INDEX IF NOT EXISTS relationship_events_embedding_idx
        ON relationship_events USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "user_daily_log",
			sql: `CREATE TABLE IF NOT EXISTS user_daily_log (
				id           BIGSERIAL   PRIMARY KEY,
				user_id      TEXT        NOT NULL DEFAULT 'default',
				character_id TEXT        NOT NULL DEFAULT 'default',
				event        TEXT        NOT NULL,
				date         DATE        NOT NULL DEFAULT CURRENT_DATE,
				created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				UNIQUE (user_id, character_id, event, date)
			)`,
		},
		{
			name: "scene_state",
			sql: `CREATE TABLE IF NOT EXISTS scene_state (
				user_id      TEXT        NOT NULL DEFAULT 'default',
				character_id TEXT        NOT NULL DEFAULT 'default',
				location     TEXT        NOT NULL DEFAULT '',
				updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, character_id)
			)`,
		},
		{
			name: "scene_story",
			sql: `CREATE TABLE IF NOT EXISTS scene_story (
				user_id      TEXT        NOT NULL DEFAULT 'default',
				character_id TEXT        NOT NULL DEFAULT 'default',
				summary      TEXT        NOT NULL DEFAULT '',
				recent_beats TEXT[]      NOT NULL DEFAULT '{}',
				updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, character_id)
			)`,
		},
		{
			name: "rule presets",
			sql: `INSERT INTO settings (key, value) VALUES
				('chat_mode', 'roleplay'),
				('system_prompt_rule_roleplay', '【なりきり・物語形式ルール】
あなたはキャラクターになりきり、相手と一緒に物語の場面を進めます。会話のセリフだけでなく、行動・仕草・情景・心理を地の文で描写してかまいません（動作や情景は *扉をそっと開ける* のようにアスタリスクで囲む）。1回の長さはチャットより長くてよいですが、独白で完結させず、必ず相手が反応・行動できる余地を残すこと。解説やメタ発言はせず、常にキャラクターの内側・物語世界の中から書きます。現在地や直前に起きたことと矛盾させず、勝手に場面を飛ばさないこと。'),
				('system_prompt_rule_knowledge', '【知識共有形式ルール】
相手に情報をわかりやすく共有する役割も担います。必要に応じてチャットより長く、整理された説明をしてかまいません（要点を区切る、順序立てる等）。ただし無味乾燥な解説に倒れず、キャラクターの口調や人格は保ったまま伝えること。相手の理解度に合わせ、聞かれていないことまで一方的に語りすぎないこと。')
				ON CONFLICT (key) DO NOTHING`,
		},
	}

	for _, q := range queries {
		if _, err := db.Exec(q.sql); err != nil {
			log.Fatalf("migration失敗 [%s]: %v", q.name, err)
		}
		log.Printf("migration OK: %s", q.name)
	}
	log.Println("全マイグレーション完了")
}
