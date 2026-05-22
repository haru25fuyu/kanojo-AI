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
				role            VARCHAR(16) NOT NULL CHECK (role IN ('user', 'assistant')),
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
				conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
				topic_id        UUID NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
				summary         TEXT NOT NULL DEFAULT '',
				date            DATE NOT NULL DEFAULT CURRENT_DATE,
				PRIMARY KEY (conversation_id, topic_id)
			)`,
		},
		{
			name: "partner_status",
			sql: `CREATE TABLE IF NOT EXISTS partner_status (
				user_id      TEXT        NOT NULL DEFAULT 'default',
				character_id TEXT        NOT NULL DEFAULT 'default',
				affection    INT         NOT NULL DEFAULT 50  CHECK (affection  BETWEEN 0 AND 100),
				trust        INT         NOT NULL DEFAULT 10  CHECK (trust      BETWEEN 0 AND 100),
				fatigue      INT         NOT NULL DEFAULT 0   CHECK (fatigue    BETWEEN 0 AND 100),
				mood         INT         NOT NULL DEFAULT 0   CHECK (mood       BETWEEN -100 AND 100),
				stress       INT         NOT NULL DEFAULT 0   CHECK (stress     BETWEEN 0 AND 100),
				energy       INT         NOT NULL DEFAULT 100 CHECK (energy     BETWEEN 0 AND 100),
				updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, character_id)
			)`,
		},
		{
			name: "default partner_status",
			sql:  `INSERT INTO partner_status (user_id, character_id) VALUES ('default', 'saya') ON CONFLICT DO NOTHING`,
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
				key           TEXT        NOT NULL,
				value         TEXT        NOT NULL,
				importance    FLOAT       NOT NULL DEFAULT 0.5,
				mention_count INT         NOT NULL DEFAULT 1,
				embedding     vector(1536),
				updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				PRIMARY KEY (user_id, key)
			)`,
		},
		{
			name: "user_info embedding index",
			sql: `CREATE INDEX IF NOT EXISTS user_info_embedding_idx
				ON user_info USING hnsw (embedding vector_cosine_ops)`,
		},
		{
			name: "schedules",
			sql: `CREATE TABLE IF NOT EXISTS schedules (
				id           BIGSERIAL   PRIMARY KEY,
				user_id      TEXT        NOT NULL DEFAULT 'default',
				label        TEXT        NOT NULL,
				date         DATE        NOT NULL,
				repeat       BOOLEAN     NOT NULL DEFAULT FALSE,
				notified     BOOLEAN     NOT NULL DEFAULT FALSE,
				created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
				('avg_threshold',       '0.38'),
				('max_threshold',       '0.50'),
				('topic_threshold',     '0.50'),
				('model_chat',          'gemini-3-flash-preview'),
				('model_batch',         'gemini-3.1-flash-lite'),
				('system_prompt_rule',  'あなたは人間の女性として自然な日常会話をしてください。AIや機械であることを意識させる発言は絶対にしない。専門用語や難しい知識をひけらかさない。日常的な話題に徹する。'),
				('debug_mode',          'true'),
				('proactive_hour_start','8'),
				('proactive_hour_end',  '22'),
				('user_info_limit',     '5')
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
	}

	for _, q := range queries {
		if _, err := db.Exec(q.sql); err != nil {
			log.Fatalf("migration失敗 [%s]: %v", q.name, err)
		}
		log.Printf("migration OK: %s", q.name)
	}
	log.Println("全マイグレーション完了")
}