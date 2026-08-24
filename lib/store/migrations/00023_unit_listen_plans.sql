-- +goose Up
CREATE TABLE unit_listen_plans (
  unit_id text NOT NULL REFERENCES units(unit_id) ON DELETE CASCADE,
  version bigint NOT NULL CHECK (version > 0),
  envelope jsonb NOT NULL,
  signed boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (unit_id, version)
);

-- +goose Down
DROP TABLE IF EXISTS unit_listen_plans;
