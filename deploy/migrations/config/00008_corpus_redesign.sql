-- +goose Up

-- A taxonomy release freezes the subject vocabulary used by one corpus build.
-- Releases are immutable. A new version creates a new release and taxon rows.
CREATE TABLE config.taxonomy_release (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    taxonomy_key        TEXT NOT NULL,
    version             TEXT NOT NULL,
    content_digest      TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_taxonomy_release UNIQUE (taxonomy_key, version),
    CONSTRAINT uq_config_taxonomy_release_digest UNIQUE (taxonomy_key, content_digest),
    CONSTRAINT chk_config_taxonomy_release_digest CHECK (content_digest <> '')
);

-- parent_id is an in-release hierarchy edge. subject_key is stable only inside
-- one taxonomy release, so consumers always carry taxonomy_release_id with it.
CREATE TABLE config.subject_taxon (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    taxonomy_release_id BIGINT NOT NULL,
    subject_key         TEXT NOT NULL,
    parent_id           BIGINT,
    label               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    ordinal             INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_config_subject_taxon_release FOREIGN KEY (taxonomy_release_id)
        REFERENCES config.taxonomy_release (id),
    CONSTRAINT uq_config_subject_taxon UNIQUE (taxonomy_release_id, subject_key),
    CONSTRAINT uq_config_subject_taxon_release_id UNIQUE (taxonomy_release_id, id),
    CONSTRAINT fk_config_subject_taxon_parent FOREIGN KEY (taxonomy_release_id, parent_id)
        REFERENCES config.subject_taxon (taxonomy_release_id, id),
    CONSTRAINT chk_config_subject_taxon_key CHECK (subject_key <> '')
);

CREATE INDEX idx_config_subject_taxon_parent ON config.subject_taxon (taxonomy_release_id, parent_id);

-- +goose StatementBegin
CREATE FUNCTION config.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER taxonomy_release_immutable
BEFORE UPDATE OR DELETE ON config.taxonomy_release
FOR EACH ROW EXECUTE FUNCTION config.reject_immutable_change();

CREATE TRIGGER subject_taxon_immutable
BEFORE UPDATE OR DELETE ON config.subject_taxon
FOR EACH ROW EXECUTE FUNCTION config.reject_immutable_change();
