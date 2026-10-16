package management_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/config"
)

func TestManagementRejectsInvalidJSONBodyBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   func(managementTestEnv) string
		body   func(managementTestEnv) string
	}{
		{"create key", http.MethodPost, func(managementTestEnv) string { return "/api/management/keys" }, func(managementTestEnv) string { return `{"display_name":"Must not create"}` }},
		{"patch key", http.MethodPatch, func(env managementTestEnv) string { return "/api/management/keys/" + env.adminUserID }, func(managementTestEnv) string { return `{"display_name":"Must not update"}` }},
		{"public URL", http.MethodPost, func(managementTestEnv) string {
			return "/api/management/buckets/photos/objects/2026/image.jpg/public-url"
		}, func(managementTestEnv) string { return `{}` }},
		{"share link", http.MethodPost, func(managementTestEnv) string { return "/api/management/share-links" }, func(managementTestEnv) string { return `{"bucket":"photos","key":"2026/image.jpg"}` }},
		{"create grant", http.MethodPost, func(managementTestEnv) string { return "/api/management/buckets/photos/grants" }, func(env managementTestEnv) string {
			return fmt.Sprintf(`{"grantee_user_id":%q,"actions":["s3:GetObject"]}`, env.memberUserID)
		}},
		{"transfer ownership", http.MethodPost, func(managementTestEnv) string { return "/api/management/buckets/photos/transfer-ownership" }, func(env managementTestEnv) string {
			return fmt.Sprintf(`{"new_owner_user_id":%q}`, env.memberUserID)
		}},
	}
	for _, test := range tests {
		for _, suffix := range []struct{ name, body string }{
			{"second document", `{}`},
			{"trailing garbage", `garbage`},
			{"oversized chunked body", strings.Repeat(" ", 1<<20)},
		} {
			t.Run(test.name+"/"+suffix.name, func(t *testing.T) {
				cfg := config.Default()
				cfg.PublicReadSigningSecret = strings.Repeat("a", 32)
				env := newManagementTestEnvWithConfig(t, cfg)
				request := httptest.NewRequest(test.method, test.path(env), strings.NewReader(test.body(env)+suffix.body))
				request.ContentLength = -1
				request.Header.Set("Authorization", "Bearer "+env.adminToken)
				response := httptest.NewRecorder()
				env.router.ServeHTTP(response, request)
				if response.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
				}
				var userCount, shareCount, grantCount int
				if err := env.db.QueryRow(`SELECT (SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM share_links), (SELECT COUNT(*) FROM grants)`).Scan(&userCount, &shareCount, &grantCount); err != nil {
					t.Fatal(err)
				}
				if userCount != 2 || shareCount != 0 || grantCount != 0 {
					t.Fatalf("invalid body changed rows: users=%d shares=%d grants=%d", userCount, shareCount, grantCount)
				}
				var name, owner string
				if err := env.db.QueryRow(`SELECT display_name FROM users WHERE id = ?`, env.adminUserID).Scan(&name); err != nil {
					t.Fatal(err)
				}
				if err := env.db.QueryRow(`SELECT owner_id FROM buckets WHERE name = 'photos'`).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				if name != "Admin User" || owner != env.adminUserID {
					t.Fatalf("invalid body updated key/owner: %q, %q", name, owner)
				}
			})
		}
	}
}
