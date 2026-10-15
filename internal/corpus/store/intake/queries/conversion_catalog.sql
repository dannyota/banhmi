-- The one advisory-lock exception serializes every blob-inserting writer.
-- Admission, lease and source parents precede it; blob locks follow it.
-- name: AcquireBlobCatalogGuard :exec
SELECT pg_advisory_xact_lock(1129271888, 2);

-- Discover and prelock existing rows in hash order before inserting any row.
-- name: LockCatalogBlob :one
SELECT id FROM bronze.blob WHERE sha256 = $1 FOR UPDATE;

-- No winner lookup here: an empty result requires rollback and rediscovery.
-- name: InsertCatalogBlob :one
INSERT INTO bronze.blob
    (sha256, byte_size, media_type, object_ref, capture_source, capture_ref, captured_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (sha256) DO NOTHING
RETURNING id;

-- Both fresh insertion and replay verify protection through a fresh snapshot.
-- name: FindCatalogExecutionEdge :one
SELECT id FROM bronze.blob_retention_reference
WHERE blob_id = $1 AND owner_kind = 'execution' AND owner_key = $2;

-- Receipt provenance always belongs to the stored result's execution.
-- name: ReadCatalogResultExecution :one
SELECT execution_key, run_receipts FROM ingest.execution WHERE id = $1;
