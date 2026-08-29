// Package gitauth resolves the GitHub credential that authenticates work done
// on behalf of an organization — cloning a repo, probing a merge.
//
// The ordering below IS the isolation guarantee: a real (multi-member)
// organization with no credential of its own resolves to NOTHING. It must never
// fall back to the triggering member's personal token, which is the bug this
// replaces — a project created by a member silently made that member's personal
// credential into company infrastructure. Mirrors chooseOrgCredential in Nest.
package gitauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

type Source string

const (
	SourceOrganization     Source = "organization"
	SourcePersonalOrgOwner Source = "personal-org-owner"
	SourceNone             Source = "none"
)

// Queries is narrow on purpose: a test can assert which of the three the
// resolver did and — for a shared org — did NOT issue.
type Queries interface {
	GetProjectGitAuthContext(ctx context.Context, id pgtype.UUID) (dbgen.GetProjectGitAuthContextRow, error)
	GetOrgGitCredential(ctx context.Context, organizationID pgtype.UUID) (dbgen.GetOrgGitCredentialRow, error)
	GetPersonalGitCredential(ctx context.Context, userID pgtype.UUID) (dbgen.GetPersonalGitCredentialRow, error)
}

type Resolver struct {
	q      Queries
	cipher *crypto.Cipher
}

func NewResolver(q Queries, cipher *crypto.Cipher) *Resolver {
	return &Resolver{q: q, cipher: cipher}
}

// Credential carries the decrypted token and where it came from. An absent
// credential is Source none with an empty Token and a nil error — for a shared
// organization that is a refusal, not a failure.
type Credential struct {
	Token  string
	Source Source
}

func (r *Resolver) ForProject(ctx context.Context, projectID string) (Credential, error) {
	none := Credential{Source: SourceNone}
	if projectID == "" {
		return none, nil
	}
	id, err := db.ParseUUID(projectID)
	if err != nil {
		return none, err
	}

	org, err := r.q.GetProjectGitAuthContext(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, nil
		}
		return none, fmt.Errorf("load git auth context: %w", err)
	}

	cred, err := r.q.GetOrgGitCredential(ctx, org.OrganizationID)
	if err == nil {
		return r.decrypt(cred.ID, cred.SecretEnc, SourceOrganization)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return none, fmt.Errorf("load organization git credential: %w", err)
	}

	// A shared org stops here. Its members' personal credentials are exactly
	// what must not be borrowed, so they are never even read.
	if !org.IsPersonal || !org.OwnerUserID.Valid {
		return none, nil
	}

	own, err := r.q.GetPersonalGitCredential(ctx, org.OwnerUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, nil
		}
		return none, fmt.Errorf("load personal git credential: %w", err)
	}
	return r.decrypt(own.ID, own.SecretEnc, SourcePersonalOrgOwner)
}

// decrypt keeps Source on the failure path so a caller can say "the credential
// is broken" rather than "there is none". The token itself never reaches err.
func (r *Resolver) decrypt(id pgtype.UUID, secretEnc string, src Source) (Credential, error) {
	token, err := r.cipher.Decrypt(secretEnc)
	if err != nil {
		return Credential{Source: src}, fmt.Errorf("decrypt git credential %s: %w", db.UUIDString(id), err)
	}
	return Credential{Token: token, Source: src}, nil
}
