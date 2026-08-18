DO $$
BEGIN
    RAISE EXCEPTION 'migration 000006 is irreversible: dropping capability digests would remove target authorization';
END $$;
