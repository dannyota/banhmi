-- Freeze inherited ledger rows for upgrade retention only. No-op replay uses complete ledgers.
SELECT format('SELECT %L, coalesce(jsonb_agg(to_jsonb(row_value) ORDER BY id), ''[]''::jsonb) FROM %I.%I AS row_value%s;',
              n.nspname || '.' || c.relname, n.nspname, c.relname,
              CASE WHEN n.nspname = 'public' AND :'freeze_ledger'::boolean THEN
                  format(' WHERE id <= %s', (xpath('/row/max/text()', query_to_xml(format('SELECT coalesce(max(id), 0) AS max FROM %I.%I', n.nspname, c.relname), false, true, '')))[1]::text)
              ELSE '' END)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND (n.nspname IN ('bronze','silver','gold','ingest','config')
    OR (n.nspname = 'public' AND c.relname LIKE 'goose_db_version%'))
ORDER BY n.nspname, c.relname;
SELECT format('SELECT %L, last_value, is_called FROM %I.%I;', n.nspname || '.' || c.relname, n.nspname, c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'S' AND n.nspname IN ('bronze','silver','gold','ingest','config')
ORDER BY n.nspname, c.relname;
