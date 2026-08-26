-- +goose Up
-- +goose StatementBegin
ALTER TABLE api_keys
	ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT false;

WITH ranked_active_keys AS (
	SELECT
		id,
		row_number() OVER (
			PARTITION BY user_id
			ORDER BY (name = 'Default') DESC, created_at ASC, id ASC
		) AS rn
	FROM api_keys
	WHERE revoked_at IS NULL
)
UPDATE api_keys AS k
SET is_default = (ranked_active_keys.rn = 1),
	updated_at = now()
FROM ranked_active_keys
WHERE k.id = ranked_active_keys.id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_one_active_default_per_user
	ON api_keys (user_id)
	WHERE revoked_at IS NULL AND is_default;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_api_keys_one_active_default_per_user;

ALTER TABLE api_keys
	DROP COLUMN IF EXISTS is_default;
-- +goose StatementEnd
