-- Use thrift group name as the user-facing identifier instead of random invite codes.
CREATE UNIQUE INDEX IF NOT EXISTS thrift_groups_name_unique_idx
    ON thrift_groups(LOWER(TRIM(name)))
    WHERE status <> 'cancelled';
