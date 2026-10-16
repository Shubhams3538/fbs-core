package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/iam"
)

func TestSignedManagementBodyIntegrity(t *testing.T) {
	const original = `{"display_name":"Delegated member","role":"member"}`
	const changed = `{"display_name":"Delegated member","role":"admin"}`
	for _, query := range []bool{false, true} {
		mode := "header"
		if query {
			mode = "query"
		}
		for _, test := range []struct {
			name    string
			payload string
			status  int
		}{
			{"valid", original, http.StatusCreated},
			{"changed role", changed, http.StatusBadRequest},
			{"removed body", "", http.StatusBadRequest},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				env := newDownloadTestEnv(t)
				credentials := integritySigner(t, env)
				before, err := env.app.objects.Users.List(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				request := integrityRequest(http.MethodPost, "/api/management/keys", test.payload, integrityHash(original), credentials, query)
				request.ContentLength = -1
				integrityServe(t, env, request, test.status)
				after, err := env.app.objects.Users.List(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				wantCount := len(before)
				if test.status == http.StatusCreated {
					wantCount++
				}
				if len(after) != wantCount {
					t.Fatalf("persisted users = %d, want %d", len(after), wantCount)
				}
				for _, user := range after {
					if user.DisplayName == "Delegated member" && user.Role != iam.RoleMember {
						t.Fatalf("changed role persisted: %s", user.Role)
					}
				}
			})
		}
	}
}

func TestManagementBodyIntegrityCompatibility(t *testing.T) {
	for _, mode := range []string{"bearer", "dev", "header unsigned", "query unsigned"} {
		t.Run(mode, func(t *testing.T) {
			env := newDownloadTestEnv(t)
			credentials := integritySigner(t, env)
			const body = `{"display_name":"Compatibility member","role":"member"}`
			request := httptest.NewRequest(http.MethodPost, "/api/management/keys", strings.NewReader(body))
			switch mode {
			case "bearer":
				request.Header.Set("Authorization", "Bearer "+env.token)
			case "dev":
				env.app.authChain.Authenticators = append([]auth.Authenticator{&auth.DevAuthenticator{}}, env.app.authChain.Authenticators...)
			case "header unsigned", "query unsigned":
				request = integrityRequest(http.MethodPost, "/api/management/keys", body, "UNSIGNED-PAYLOAD", credentials, mode == "query unsigned")
			}
			integrityServe(t, env, request, http.StatusCreated)
		})
	}
}

func TestSignedOptionalControlBodyCannotBeRemoved(t *testing.T) {
	for _, query := range []bool{false, true} {
		for _, signedPayload := range []string{"", `{"expires_in_seconds":60}`} {
			env := newDownloadTestEnv(t)
			credentials := integritySigner(t, env)
			request := integrityRequest(http.MethodPost, "/api/management/buckets/downloads/objects/video.mp4/public-url", "", integrityHash(signedPayload), credentials, query)
			request.Body = nil
			wantStatus := http.StatusOK
			if signedPayload != "" {
				wantStatus = http.StatusBadRequest
			}
			integrityServe(t, env, request, wantStatus)
		}
	}
}
