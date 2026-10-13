-- Synthetic rows in every legacy layer, including generated identities.
INSERT INTO bronze.source_document (source, external_id, discovered_at, collected_at, first_collected_at)
VALUES ('ci-legacy', 'legacy-document', '2026-01-01', '2026-01-01', '2026-01-01');
INSERT INTO silver.document (doc_key, title, created_at, updated_at)
VALUES ('ci-legacy', 'Synthetic legacy evidence', '2026-01-01', '2026-01-01');
INSERT INTO gold.chunk (document_id, citation, content, ordinal)
SELECT id, 'legacy-citation', 'Synthetic retained content', 0 FROM silver.document WHERE doc_key = 'ci-legacy';
INSERT INTO ingest.discover_cursor (source, keyword, watermark, created_at, updated_at)
VALUES ('ci-legacy', 'legacy-keyword', 'retained-watermark', '2026-01-01', '2026-01-01');
INSERT INTO config.discovery_keyword (term, source) VALUES ('legacy-term', 'ci-legacy');
