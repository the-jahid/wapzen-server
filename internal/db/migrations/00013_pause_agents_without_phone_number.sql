-- +goose Up
-- +goose StatementBegin
-- Enforce the go-live invariant on existing data: an agent may only be active
-- while it has a phone number assigned, since without one it has no number to
-- place or answer calls on. Pause any agent that is currently active without a
-- number (rows created before this rule existed default their status to
-- 'active', so they show as Live despite having nothing to call from).
UPDATE agents
SET status = 'inactive',
	updated_at = now()
WHERE status = 'active'
	AND (phone_number_id IS NULL OR btrim(phone_number_id) = '');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Irreversible data backfill: we cannot tell which paused agents were active
-- before this ran, so the down migration is a no-op.
SELECT 1;
-- +goose StatementEnd
