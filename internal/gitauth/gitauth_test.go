package gitauth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

const (
	projectID = "11111111-1111-1111-1111-111111111111"
	orgID     = "22222222-2222-2222-2222-222222222222"
	ownerID   = "33333333-3333-3333-3333-333333333333"
)

// fakeQueries records every call, so a test can assert not only what the
// resolver returned but which tables it refused to touch.
type fakeQueries struct {
	isPersonal bool
	noOwner    bool
	orgSecret  string
	ownSecret  string

	orgCalls      int
	personalCalls int
	personalArg   pgtype.UUID
}

func (f *fakeQueries) GetProjectGitAuthContext(_ context.Context, _ pgtype.UUID) (dbgen.GetProjectGitAuthContextRow, error) {
	row := dbgen.GetProjectGitAuthContextRow{
		OrganizationID: db.MustUUID(orgID),
		IsPersonal:     f.isPersonal,
	}
	if !f.noOwner {
		row.OwnerUserID = db.MustUUID(ownerID)
	}
	return row, nil
}

func (f *fakeQueries) GetOrgGitCredential(_ context.Context, _ pgtype.UUID) (dbgen.GetOrgGitCredentialRow, error) {
	f.orgCalls++
	if f.orgSecret == "" {
		return dbgen.GetOrgGitCredentialRow{}, pgx.ErrNoRows
	}
	return dbgen.GetOrgGitCredentialRow{ID: db.MustUUID(orgID), SecretEnc: f.orgSecret}, nil
}

func (f *fakeQueries) GetPersonalGitCredential(_ context.Context, userID pgtype.UUID) (dbgen.GetPersonalGitCredentialRow, error) {
	f.personalCalls++
	f.personalArg = userID
	if f.ownSecret == "" {
		return dbgen.GetPersonalGitCredentialRow{}, pgx.ErrNoRows
	}
	return dbgen.GetPersonalGitCredentialRow{ID: db.MustUUID(ownerID), SecretEnc: f.ownSecret}, nil
}

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

func seal(t *testing.T, c *crypto.Cipher, plain string) string {
	t.Helper()
	enc, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

func TestOrgCredentialWins(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{orgSecret: seal(t, c, "org-token"), ownSecret: seal(t, c, "personal-token")}

	got, err := NewResolver(q, c).ForProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if got.Token != "org-token" || got.Source != SourceOrganization {
		t.Fatalf("got %+v, want org-token/%s", got, SourceOrganization)
	}
	if q.personalCalls != 0 {
		t.Fatalf("personal credential queried %d times despite an org credential", q.personalCalls)
	}
}

func TestPersonalOrgFallsBackToOwner(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{isPersonal: true, ownSecret: seal(t, c, "personal-token")}

	got, err := NewResolver(q, c).ForProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if got.Token != "personal-token" || got.Source != SourcePersonalOrgOwner {
		t.Fatalf("got %+v, want personal-token/%s", got, SourcePersonalOrgOwner)
	}
	if q.orgCalls != 1 {
		t.Fatalf("org credential should still be tried first, calls = %d", q.orgCalls)
	}
	if db.UUIDString(q.personalArg) != ownerID {
		t.Fatalf("fell back to %q, want the org owner %q", db.UUIDString(q.personalArg), ownerID)
	}
}

// The security property: a shared organization with no credential of its own
// resolves to nothing, and a member's personal token is not even read.
func TestSharedOrgWithoutCredentialRefuses(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{isPersonal: false, ownSecret: seal(t, c, "personal-token")}

	got, err := NewResolver(q, c).ForProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if got.Token != "" || got.Source != SourceNone {
		t.Fatalf("got %+v, want no token/%s", got, SourceNone)
	}
	if q.personalCalls != 0 {
		t.Fatalf("personal credential was queried %d times in a shared org", q.personalCalls)
	}
}

func TestPersonalOrgWithNoCredentialAnywhere(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{isPersonal: true}

	got, err := NewResolver(q, c).ForProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if got.Token != "" || got.Source != SourceNone {
		t.Fatalf("got %+v, want no token/%s", got, SourceNone)
	}
	if q.personalCalls != 1 {
		t.Fatalf("personal org must consult its owner, calls = %d", q.personalCalls)
	}
}

func TestPersonalOrgWithoutOwnerDoesNotQuery(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{isPersonal: true, noOwner: true, ownSecret: seal(t, c, "personal-token")}

	got, err := NewResolver(q, c).ForProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if got.Source != SourceNone || q.personalCalls != 0 {
		t.Fatalf("got %+v with %d personal calls, want none", got, q.personalCalls)
	}
}

func TestUndecryptableCredentialKeepsItsSourceAndLeaksNothing(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{orgSecret: "not-a-ciphertext"}

	got, err := NewResolver(q, c).ForProject(context.Background(), projectID)
	if err == nil {
		t.Fatal("want an error for an undecryptable credential")
	}
	if got.Token != "" || got.Source != SourceOrganization {
		t.Fatalf("got %+v, want empty token with source %s", got, SourceOrganization)
	}
	if !strings.Contains(err.Error(), orgID) {
		t.Fatalf("error should name the credential: %v", err)
	}
	if strings.Contains(err.Error(), "not-a-ciphertext") {
		t.Fatalf("secret material reached the error: %v", err)
	}
}

func TestEmptyProjectQueriesNothing(t *testing.T) {
	c := testCipher(t)
	q := &fakeQueries{orgSecret: seal(t, c, "org-token"), ownSecret: seal(t, c, "personal-token")}

	got, err := NewResolver(q, c).ForProject(context.Background(), "")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if got.Source != SourceNone || q.orgCalls != 0 || q.personalCalls != 0 {
		t.Fatalf("got %+v with %d org / %d personal calls, want none", got, q.orgCalls, q.personalCalls)
	}
}

func TestContextLookupErrorIsNotSilentlyNone(t *testing.T) {
	c := testCipher(t)
	q := &erroringQueries{}

	if _, err := NewResolver(q, c).ForProject(context.Background(), projectID); err == nil {
		t.Fatal("a database failure must not read as an absent credential")
	}
}

type erroringQueries struct{ fakeQueries }

func (e *erroringQueries) GetProjectGitAuthContext(_ context.Context, _ pgtype.UUID) (dbgen.GetProjectGitAuthContextRow, error) {
	return dbgen.GetProjectGitAuthContextRow{}, errors.New("connection refused")
}
