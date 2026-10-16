package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func TestBearerSkipsOtherSchemesBeforeTokenValidation(t *testing.T) {
	for _, header := range []string{
		"AWS4-HMAC-SHA256 Credential=bad, SignedHeaders=host, Signature=abc",
		"Basic credential extra-field",
		"Digest username=alice, realm=fbs, response=abc",
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", header)
		if _, err := (&BearerAuthenticator{}).Authenticate(request); !errors.Is(err, ErrNotApplicable) {
			t.Errorf("scheme must be skipped for %q: %v", header, err)
		}
	}
}

func TestChainPreservesSigV4Failure(t *testing.T) {
	for _, test := range []struct {
		name      string
		active    bool
		wrongKey  bool
		repoFails bool
		want      error
	}{
		{"inactive user", false, false, false, ErrInactiveUser},
		{"invalid signature", true, true, false, ErrInvalidCredentials},
		{"repository failure", true, false, true, ErrInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, cleanup := setupTestDB(t)
			defer cleanup()
			user, _ := createTestUser(t, metadata.NewUserRepository(db), test.name, "member", test.active)
			sigv4 := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db)}
			if test.repoFails {
				sigv4.Repo = &failingUserRepo{}
			}
			secret := user.SigV4SecretKey
			if test.wrongKey {
				secret = "incorrect signing key"
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			SignRequest(request, user.SigV4AccessKeyID, secret, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)
			chain := &ChainAuthenticator{Authenticators: []Authenticator{
				&BearerAuthenticator{Repo: metadata.NewUserRepository(db)}, sigv4,
			}}
			if _, err := chain.Authenticate(request); !errors.Is(err, test.want) {
				t.Fatalf("chain error = %v, want %v", err, test.want)
			}
		})
	}
}
