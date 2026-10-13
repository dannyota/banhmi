-- Verify sequence allocation continues after retained values.
DO $$
DECLARE item record; previous bigint; called boolean; allocated bigint;
BEGIN
    FOR item IN SELECT n.nspname, c.relname, s.seqincrement
                FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid
                JOIN pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname IN ('bronze','silver','gold','ingest','config')
    LOOP
        EXECUTE format('SELECT last_value, is_called FROM %I.%I', item.nspname, item.relname)
            INTO previous, called;
        allocated := nextval(format('%I.%I', item.nspname, item.relname)::regclass);
        IF allocated <> previous + (CASE WHEN called THEN item.seqincrement ELSE 0 END) THEN
            RAISE EXCEPTION 'sequence allocation changed';
        END IF;
    END LOOP;
END $$;
