-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
	ALTER COLUMN model_temperature SET DEFAULT 0.3;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	ALTER COLUMN model_temperature DROP DEFAULT;
-- +goose StatementEnd
