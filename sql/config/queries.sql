-- Load queries (read config into the app at startup).

-- name: ListScopeTerms :many
SELECT term, term_class FROM config.scope_term WHERE enabled AND jurisdiction = $1 ORDER BY term_class, term;

-- name: ListDiscoveryKeywords :many
-- Keywords for a source plus the source-agnostic ones ('').
SELECT term FROM config.discovery_keyword
WHERE enabled AND source IN ($1, '') ORDER BY term;

-- name: SbvAgencyIDs :many
SELECT code FROM config.issuer_code WHERE source = $1 AND is_sbv ORDER BY code;

-- name: ListIssuerCodes :many
SELECT source, code, name, in_scope, is_sbv FROM config.issuer_code ORDER BY source, code;

-- Seed queries (cmd/seed). Each re-seed deletes the managed ('seed') rows and
-- re-inserts from the CSV; ON CONFLICT DO NOTHING means a user override sharing a
-- natural key is preserved. origin='user' rows are never deleted.

-- name: DeleteSeedScopeTerms :exec
DELETE FROM config.scope_term WHERE origin = 'seed';

-- name: InsertSeedScopeTerm :exec
INSERT INTO config.scope_term (jurisdiction, term, term_class, theme, origin)
VALUES ($1, $2, $3, $4, 'seed') ON CONFLICT (jurisdiction, term_class, term) DO NOTHING;

-- name: DeleteSeedIssuerCodes :exec
DELETE FROM config.issuer_code WHERE origin = 'seed';

-- name: InsertSeedIssuerCode :exec
INSERT INTO config.issuer_code (source, code, name, in_scope, is_sbv, origin)
VALUES ($1, $2, $3, $4, $5, 'seed') ON CONFLICT (source, code) DO NOTHING;

-- name: DeleteSeedDiscoveryKeywords :exec
DELETE FROM config.discovery_keyword WHERE origin = 'seed';

-- name: InsertSeedDiscoveryKeyword :exec
INSERT INTO config.discovery_keyword (term, source, origin)
VALUES ($1, $2, 'seed') ON CONFLICT (source, term) DO NOTHING;

-- name: ListValidityStatuses :many
SELECT source, code, status_class, is_current_law FROM config.validity_status ORDER BY source, code;

-- name: DeleteSeedValidityStatuses :exec
DELETE FROM config.validity_status WHERE origin = 'seed';

-- name: InsertSeedValidityStatus :exec
INSERT INTO config.validity_status (source, code, status_class, is_current_law, origin)
VALUES ($1, $2, $3, $4, 'seed') ON CONFLICT (source, code) DO NOTHING;

-- name: ListRelationTypes :many
SELECT source, code, label, is_amending FROM config.relation_type ORDER BY source, code;

-- name: DeleteSeedRelationTypes :exec
DELETE FROM config.relation_type WHERE origin = 'seed';

-- name: InsertSeedRelationType :exec
INSERT INTO config.relation_type (source, code, label, is_amending, origin)
VALUES ($1, $2, $3, $4, 'seed') ON CONFLICT (source, code) DO NOTHING;

-- name: ListSettings :many
SELECT key, value FROM config.setting;

-- name: DeleteSeedSettings :exec
DELETE FROM config.setting WHERE origin = 'seed';

-- name: InsertSeedSetting :exec
INSERT INTO config.setting (key, value, origin)
VALUES ($1, $2, 'seed') ON CONFLICT (key) DO NOTHING;
