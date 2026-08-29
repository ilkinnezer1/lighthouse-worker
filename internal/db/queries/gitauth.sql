-- name: GetProjectGitAuthContext :one
SELECT o.id AS organization_id,
       o.is_personal,
       (
         SELECT m.user_id
         FROM org_memberships m
         WHERE m.org_id = o.id
           AND m.role = 'OWNER'
         ORDER BY m.created_at
         LIMIT 1
       ) AS owner_user_id
FROM projects p
JOIN organizations o ON o.id = p.organization_id
WHERE p.id = $1;

-- name: GetOrgGitCredential :one
SELECT id, secret_enc
FROM git_credentials
WHERE organization_id = $1
  AND kind = 'PAT'
ORDER BY created_at DESC
LIMIT 1;

-- name: GetPersonalGitCredential :one
SELECT id, secret_enc
FROM git_credentials
WHERE user_id = $1
  AND organization_id IS NULL
  AND kind = 'PAT'
ORDER BY created_at DESC
LIMIT 1;
