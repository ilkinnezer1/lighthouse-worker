-- name: GetGithubTokenForOwner :one
SELECT access_token_enc
FROM user_identities
WHERE user_id = $1
  AND provider = 'GITHUB'
  AND access_token_enc IS NOT NULL
ORDER BY updated_at DESC
LIMIT 1;
