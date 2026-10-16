package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func presignedQuery(user *metadata.User, signedAt time.Time) url.Values {
	return url.Values{
		"X-Amz-Algorithm":     {sigV4Algorithm},
		"X-Amz-Credential":    {user.SigV4AccessKeyID + "/" + signedAt.Format("20060102") + "/us-east-1/s3/aws4_request"},
		"X-Amz-Date":          {signedAt.Format("20060102T150405Z")},
		"X-Amz-Expires":       {"3600"},
		"X-Amz-SignedHeaders": {"host"},
	}
}

// Sign exactly the supplied query, including deliberately missing or duplicate values.
func signPresignedQuery(req *http.Request, secretKey string, signedAt time.Time, q url.Values) {
	req.URL.RawQuery = q.Encode()
	canonicalRequest := buildCanonicalRequestForQuery(req, q.Get("X-Amz-SignedHeaders"), unsignedPayload)
	stringToSign := buildStringToSign(q.Get("X-Amz-Date"), q.Get("X-Amz-Credential"), canonicalRequest)
	q.Set("X-Amz-Signature", computeSignature(secretKey, signedAt.Format("20060102"), "us-east-1", "s3", stringToSign))
	req.URL.RawQuery = q.Encode()
}

func TestSigV4AuthQueryString(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		offset time.Duration
		query  url.Values
		want   error
	}{
		{name: "valid"},
		{name: "older_than_clock_skew_within_expiry", offset: -30 * time.Minute},
		{name: "expired", offset: -2 * time.Hour, query: url.Values{"X-Amz-Expires": {"60"}}, want: ErrInvalidCredentials},
		{name: "maximum_ttl", query: url.Values{"X-Amz-Expires": {"604800"}}},
		{name: "expires_at_now", offset: -time.Hour},
		{name: "past_expiry", offset: -time.Hour - time.Second, want: ErrInvalidCredentials},
		{name: "older_than_seven_days", offset: -7*24*time.Hour - time.Second, query: url.Values{"X-Amz-Expires": {"604800"}}, want: ErrInvalidCredentials},
		{name: "future_within_skew", offset: 15 * time.Minute},
		{name: "future_beyond_skew", offset: 15*time.Minute + time.Second, want: ErrInvalidCredentials},
		{name: "missing_algorithm", query: url.Values{"X-Amz-Algorithm": nil}, want: ErrNotApplicable},
		{name: "wrong_algorithm", query: url.Values{"X-Amz-Algorithm": {"AWS4-HMAC-SHA1"}}, want: ErrMalformedToken},
		{name: "missing_credential", query: url.Values{"X-Amz-Credential": nil}, want: ErrMalformedToken},
		{name: "missing_date", query: url.Values{"X-Amz-Date": nil}, want: ErrMalformedToken},
		{name: "malformed_date", query: url.Values{"X-Amz-Date": {"not-a-date"}}, want: ErrMalformedToken},
		{name: "credential_date_mismatch", query: url.Values{"X-Amz-Date": {"20261015T120000Z"}}, want: ErrInvalidCredentials},
		{name: "malformed_credential", query: url.Values{"X-Amz-Credential": {"bad"}}, want: ErrMalformedToken},
		{name: "missing_expires", offset: -20 * time.Minute, query: url.Values{"X-Amz-Expires": nil}, want: ErrMalformedToken},
		{name: "zero_expires", query: url.Values{"X-Amz-Expires": {"0"}}, want: ErrMalformedToken},
		{name: "negative_expires", query: url.Values{"X-Amz-Expires": {"-1"}}, want: ErrMalformedToken},
		{name: "nonnumeric_expires", query: url.Values{"X-Amz-Expires": {"tomorrow"}}, want: ErrMalformedToken},
		{name: "expires_too_large", query: url.Values{"X-Amz-Expires": {"604801"}}, want: ErrMalformedToken},
		{name: "missing_signed_headers", query: url.Values{"X-Amz-SignedHeaders": nil}, want: ErrMalformedToken},
		{name: "missing_host_in_signed_headers", query: url.Values{"X-Amz-SignedHeaders": {"x-amz-content-sha256"}}, want: ErrMalformedToken},
		{name: "signed_duplicate_query_values", query: url.Values{"partNumber": {"2", "1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, cleanup := setupTestDB(t)
			defer cleanup()
			user, _ := createTestUser(t, metadata.NewUserRepository(db), "Query User", "member", true)
			auth := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db), Now: func() time.Time { return now }}
			signedAt := now.Add(tt.offset)
			q := presignedQuery(user, signedAt)
			for key, values := range tt.query {
				q[key] = values
			}
			req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
			req.Host = "localhost:9000"
			signPresignedQuery(req, user.SigV4SecretKey, signedAt, q)

			p, err := auth.Authenticate(req)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Authenticate() error = %v, want %v", err, tt.want)
			}
			if tt.want == nil && p.UserID != user.ID {
				t.Errorf("UserID = %q, want %q", p.UserID, user.ID)
			}
		})
	}
}

func TestSigV4AuthQueryStringTampered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(url.Values)
		want   error
	}{
		{"missing_signature", func(q url.Values) { q.Del("X-Amz-Signature") }, ErrMalformedToken},
		{"nonhex_signature", func(q url.Values) { q.Set("X-Amz-Signature", "not-hex") }, ErrInvalidCredentials},
		{"short_signature", func(q url.Values) { q.Set("X-Amz-Signature", "abc") }, ErrInvalidCredentials},
		{"wrong_signature", func(q url.Values) { q.Set("X-Amz-Signature", strings.Repeat("0", 64)) }, ErrInvalidCredentials},
		{"duplicate_expires", func(q url.Values) { q.Add("X-Amz-Expires", "7200") }, ErrInvalidCredentials},
		{"duplicate_application_parameter", func(q url.Values) { q.Add("partNumber", "2") }, ErrInvalidCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, cleanup := setupTestDB(t)
			defer cleanup()
			user, _ := createTestUser(t, metadata.NewUserRepository(db), "Tampered Query", "member", true)
			auth := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db)}
			now := time.Now().UTC()
			q := presignedQuery(user, now)
			q.Set("partNumber", "1")
			req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
			req.Host = "localhost:9000"
			signPresignedQuery(req, user.SigV4SecretKey, now, q)

			tt.mutate(q)
			req.URL.RawQuery = q.Encode()
			if _, err := auth.Authenticate(req); !errors.Is(err, tt.want) {
				t.Fatalf("Authenticate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSigV4AuthSuccess(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "SigV4 User", "admin", true)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
	req.Host = "localhost:9000"

	SignRequest(req, user.SigV4AccessKeyID, user.SigV4SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	p, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", p.UserID, user.ID)
	}
	if p.Role != "admin" {
		t.Errorf("Role = %q, want admin", p.Role)
	}
	if p.DevMode {
		t.Error("expected DevMode to be false")
	}
}

func TestSigV4AuthMissingHeader(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	auth := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db)}
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	_, err := auth.Authenticate(req)
	if err != ErrNotApplicable {
		t.Fatalf("expected ErrNotApplicable, got %v", err)
	}
}

func TestSigV4AuthUnsupportedScheme(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	auth := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db)}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer token")

	_, err := auth.Authenticate(req)
	if err != ErrNotApplicable {
		t.Fatalf("expected ErrNotApplicable, got %v", err)
	}
}

func TestSigV4AuthMalformedCredential(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	auth := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db)}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=bad, SignedHeaders=host, Signature=abc")

	_, err := auth.Authenticate(req)
	if err != ErrMalformedToken {
		t.Fatalf("expected ErrMalformedToken, got %v", err)
	}
}

func TestSigV4AuthUnknownUser(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	auth := &SigV4Authenticator{Repo: metadata.NewSigV4UserRepository(db)}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9000"

	SignRequest(req, "fbsv4_unknown", "secret", "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	_, err := auth.Authenticate(req)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestSigV4AuthInactiveUser(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "Inactive", "member", false)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9000"

	SignRequest(req, user.SigV4AccessKeyID, user.SigV4SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	_, err := auth.Authenticate(req)
	if err != ErrInactiveUser {
		t.Fatalf("expected ErrInactiveUser, got %v", err)
	}
}

func TestSigV4AuthWrongSignature(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "Wrong Secret", "member", true)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9000"

	SignRequest(req, user.SigV4AccessKeyID, "wrong-secret", "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	_, err := auth.Authenticate(req)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestSigV4AuthClockSkew(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "Skew Test", "member", true)

	// Request signed 20 minutes ago
	past := time.Now().UTC().Add(-20 * time.Minute)
	auth := &SigV4Authenticator{Repo: sigv4Repo, Now: time.Now().UTC}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9000"

	SignRequestAt(req, user.SigV4AccessKeyID, user.SigV4SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash, past)

	_, err := auth.Authenticate(req)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for clock skew, got %v", err)
	}
}

func TestSigV4AuthInternalError(t *testing.T) {
	t.Parallel()

	auth := &SigV4Authenticator{Repo: &failingUserRepo{}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9000"

	SignRequest(req, "fbsv4_test", "secret", "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	_, err := auth.Authenticate(req)
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("expected ErrInternal, got %v", err)
	}
	if !strings.Contains(err.Error(), "database connection lost") {
		t.Fatalf("expected the repository cause in %v", err)
	}
}

func TestSigV4ChainAuthenticator(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "Chain User", "admin", true)

	chain := &ChainAuthenticator{
		Authenticators: []Authenticator{
			&BearerAuthenticator{Repo: repo},
			&SigV4Authenticator{Repo: sigv4Repo},
		},
	}

	// Test that a SigV4 request succeeds through the chain
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:9000"
	SignRequest(req, user.SigV4AccessKeyID, user.SigV4SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	p, err := chain.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", p.UserID, user.ID)
	}
}

func TestSigV4ChainAuthenticator_BearerFirst(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, rawToken := createTestUser(t, repo, "BearerFirst", "member", true)

	chain := &ChainAuthenticator{
		Authenticators: []Authenticator{
			&BearerAuthenticator{Repo: repo},
			&SigV4Authenticator{Repo: sigv4Repo},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)

	p, err := chain.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", p.UserID, user.ID)
	}
}

func TestSigV4AuthHeaderWithoutCommaSpace(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "No Space", "admin", true)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
	req.Host = "localhost:9000"

	// Sign normally, then rewrite the header to remove spaces after commas
	SignRequest(req, user.SigV4AccessKeyID, user.SigV4SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)
	authHeader := req.Header.Get("Authorization")
	authHeader = strings.ReplaceAll(authHeader, ", ", ",")
	req.Header.Set("Authorization", authHeader)

	p, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", p.UserID, user.ID)
	}
}

func TestSigV4AuthHeaderWithDateFallback(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "Date Fallback", "admin", true)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
	req.Host = "localhost:9000"

	// Manually sign using Date header instead of X-Amz-Date
	now := time.Now().UTC()
	timestamp := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	rfcDate := now.Format(time.RFC1123)

	req.Header.Set("Host", req.Host)
	req.Header.Set("Date", rfcDate)
	req.Header.Set("X-Amz-Content-SHA256", EmptyStringHash)

	signedHeadersStr := "date;host;x-amz-content-sha256"
	canonicalRequest := buildCanonicalRequest(req, signedHeadersStr, EmptyStringHash)
	credential := user.SigV4AccessKeyID + "/" + date + "/us-east-1/s3/aws4_request"
	stringToSign := buildStringToSign(timestamp, credential, canonicalRequest)
	signature := computeSignature(user.SigV4SecretKey, date, "us-east-1", "s3", stringToSign)

	authHeader := sigV4Algorithm +
		" Credential=" + credential +
		", SignedHeaders=" + signedHeadersStr +
		", Signature=" + signature
	req.Header.Set("Authorization", authHeader)

	p, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", p.UserID, user.ID)
	}
}

func TestSigV4AuthMissingHostInSignedHeaders(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "No Host", "admin", true)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
	req.Host = "localhost:9000"

	// Sign with a SignedHeaders list that deliberately omits "host"
	SignRequest(req, user.SigV4AccessKeyID, user.SigV4SecretKey, "us-east-1", "s3", []string{"x-amz-content-sha256", "x-amz-date"}, EmptyStringHash)

	_, err := auth.Authenticate(req)
	if err != ErrMalformedToken {
		t.Fatalf("expected ErrMalformedToken for missing host in SignedHeaders, got %v", err)
	}
}

func TestSigV4AuthInvalidCredentialScope(t *testing.T) {
	t.Parallel()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := metadata.NewUserRepository(db)
	sigv4Repo := metadata.NewSigV4UserRepository(db)
	user, _ := createTestUser(t, repo, "Scope Test", "admin", true)

	auth := &SigV4Authenticator{Repo: sigv4Repo}
	now := time.Now().UTC()
	timestamp := now.Format("20060102T150405Z")
	date := now.Format("20060102")

	tests := []struct {
		name       string
		credential string
	}{
		{"empty_access_key", "/" + date + "/us-east-1/s3/aws4_request"},
		{"bad_date_length", user.SigV4AccessKeyID + "/" + date + "01/us-east-1/s3/aws4_request"},
		{"bad_date_chars", user.SigV4AccessKeyID + "/abcdefgh/us-east-1/s3/aws4_request"},
		{"empty_region", user.SigV4AccessKeyID + "/" + date + "//s3/aws4_request"},
		{"non_s3_service", user.SigV4AccessKeyID + "/" + date + "/us-east-1/ec2/aws4_request"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test-bucket/hello.txt", nil)
			req.Host = "localhost:9000"

			authHeader := sigV4Algorithm +
				" Credential=" + tc.credential +
				", SignedHeaders=host" +
				", Signature=dummysignature"
			req.Header.Set("Authorization", authHeader)
			req.Header.Set("Host", req.Host)
			req.Header.Set("X-Amz-Date", timestamp)

			_, err := auth.Authenticate(req)
			if err != ErrMalformedToken {
				t.Fatalf("expected ErrMalformedToken for %s, got %v", tc.name, err)
			}
		})
	}
}
