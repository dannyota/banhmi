CREATE SCHEMA IF NOT EXISTS config;

-- config holds seeded defaults plus operator customizations. Rows with
-- origin='seed' are replaced by `cmd/seed` (which reads deploy/seed/*.csv);
-- origin='user' rows are never touched, so customizations survive a re-seed.
-- See docs/guides/SCOPE.md for what these drive.

-- config.scope_term feeds the scope matcher. term_class: 'strong' = in scope for
-- any issuer (title + body); 'strong_title' = in scope for any issuer but matched
-- on title only (specific terms whose body use is mostly boilerplate); 'weak' =
-- counts only with a banking signal; 'signal' = a banking-sector marker that
-- qualifies weak terms.
CREATE TABLE config.scope_term (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    jurisdiction TEXT NOT NULL DEFAULT 'vn',
    term         TEXT NOT NULL,
    term_class   TEXT NOT NULL,
    theme        TEXT NOT NULL DEFAULT '',
    origin       TEXT NOT NULL DEFAULT 'seed',
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_scope_term UNIQUE (jurisdiction, term_class, term),
    CONSTRAINT chk_config_scope_term_class CHECK (term_class IN ('strong', 'strong_title', 'weak', 'signal')),
    CONSTRAINT chk_config_scope_term_origin CHECK (origin IN ('seed', 'user'))
);

-- config.issuer_code: per-source issuer codes (congbao c-codes, vbpl agency ids).
-- is_sbv marks the State Bank issuer, which drives the agency filter.
CREATE TABLE config.issuer_code (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source     TEXT NOT NULL,
    code       TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    in_scope   BOOLEAN NOT NULL DEFAULT FALSE,
    is_sbv     BOOLEAN NOT NULL DEFAULT FALSE,
    origin     TEXT NOT NULL DEFAULT 'seed',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_issuer_code UNIQUE (source, code),
    CONSTRAINT chk_config_issuer_code_origin CHECK (origin IN ('seed', 'user'))
);

-- config.discovery_keyword: query terms for keyword-search discovery.
CREATE TABLE config.discovery_keyword (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    term       TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT '',
    origin     TEXT NOT NULL DEFAULT 'seed',
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_discovery_keyword UNIQUE (source, term),
    CONSTRAINT chk_config_discovery_keyword_origin CHECK (origin IN ('seed', 'user'))
);

-- config.setting: generic key/value store for operator-tunable thresholds (e.g.
-- the per-PDF extract-vs-OCR gate). origin seed/user follows the other config
-- tables: re-seeding replaces 'seed' rows, 'user' overrides survive.
CREATE TABLE config.setting (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    origin     TEXT NOT NULL DEFAULT 'seed',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_setting UNIQUE (key),
    CONSTRAINT chk_config_setting_origin CHECK (origin IN ('seed', 'user'))
);

-- config.validity_status maps a source effect-status code (vbpl effStatus: CHL,
-- HHL, HHL1P, CCHL, TDHL…) to banhmi's status_class, and marks which classes are
-- returnable as current law (is_current_law). It de-hardcodes both the code→class
-- mapping (pkg/pipeline normalizeValidity) and the current-law retrieval filter, so
-- the legal-currency policy and new/renamed source codes are operator-tunable
-- without a code change. status_class must match silver.validity_period's allowed
-- values. source='' applies to any source.
CREATE TABLE config.validity_status (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source         TEXT NOT NULL DEFAULT '',
    code           TEXT NOT NULL,
    status_class   TEXT NOT NULL,
    is_current_law BOOLEAN NOT NULL DEFAULT FALSE,
    origin         TEXT NOT NULL DEFAULT 'seed',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_validity_status UNIQUE (source, code),
    CONSTRAINT chk_config_validity_status_class CHECK (status_class IN ('in_force', 'expired', 'partial', 'not_yet', 'suspended', 'inappropriate')),
    CONSTRAINT chk_config_validity_status_origin CHECK (origin IN ('seed', 'user'))
);

-- config.relation_type maps a source-native relation code (vbpl referenceType
-- int, as text) to banhmi's relation_type label, and marks which labels count as
-- an amendment for incoming-amendment evidence (is_amending). It de-hardcodes both
-- the code→label mapping (pkg/ingest/vbpl) and the amending-type set used by the
-- MCP document tool, so relation semantics are operator-tunable in the DB. Codes
-- with no row decode to a neutral "<source>_type_<code>" label (not guessed).
CREATE TABLE config.relation_type (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source      TEXT NOT NULL,
    code        TEXT NOT NULL,
    label       TEXT NOT NULL,
    is_amending BOOLEAN NOT NULL DEFAULT FALSE,
    origin      TEXT NOT NULL DEFAULT 'seed',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_config_relation_type UNIQUE (source, code),
    CONSTRAINT chk_config_relation_type_origin CHECK (origin IN ('seed', 'user'))
);
