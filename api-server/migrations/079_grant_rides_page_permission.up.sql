-- Grant the new /admin/rides console page to Operations Manager.
--
-- The admin console reads its navigation and its route guard from
-- admin_roles.permissions, so a page that is not listed there is unreachable
-- even though the API route behind it (GET /api/v1/admin/rides) is already
-- open to the Operations bucket. Without this, only Super Admin ("*") could
-- see the completed-rides browser.
--
-- Idempotent: the array is only extended when the entry is missing, so
-- re-running this — or running it against a role whose permissions were edited
-- in the Admins & Roles screen — neither duplicates nor overwrites anything.

BEGIN;

UPDATE admin_roles
SET permissions = permissions || '["/admin/rides"]'::jsonb
WHERE name = 'Operations Manager'
  AND NOT permissions @> '["/admin/rides"]'::jsonb
  AND NOT permissions @> '["*"]'::jsonb;

COMMIT;
