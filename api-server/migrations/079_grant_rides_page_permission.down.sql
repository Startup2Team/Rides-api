-- Reverse of 079: revoke the /admin/rides console page from Operations Manager.
--
-- Removes only that one entry, leaving every other permission — including any
-- added later through the Admins & Roles screen — untouched.

BEGIN;

UPDATE admin_roles
SET permissions = permissions - '/admin/rides'
WHERE name = 'Operations Manager';

COMMIT;
