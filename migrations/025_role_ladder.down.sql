-- Remove the built-in role ladder. The role_permissions rows cascade from the
-- roles anyway, but they are deleted explicitly first so the intent is clear.
-- The permissions table is left untouched: the read/write rows this migration
-- introduced are harmless if they linger, and dropping them risks removing a
-- permission that 001, 024, or a future migration also relies on.
DELETE FROM role_permissions
WHERE role_id IN (
    SELECT id FROM roles WHERE name IN ('viewer', 'editor', 'operator')
);

DELETE FROM roles WHERE name IN ('viewer', 'editor', 'operator');
