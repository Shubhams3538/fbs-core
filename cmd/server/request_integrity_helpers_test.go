package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/iam"
)

func integritySigner(t *testing.T, env downloadTestEnv) auth.SigV4Credentials {
	t.Helper()
	_, credentials, _, err := auth.CreateBearerToken(context.Background(), env.app.objects.Users, "Integrity signer", iam.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func integrityHash(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func integrityRequest(method, path, payload, signedHash string, credentials auth.SigV4Credentials, query bool) *http.Request {
	request := httptest.NewRequest(method, "http://fbs.local"+path, strings.NewReader(payload))
	if query {
		values := request.URL.Query()
		values.Set("X-Amz-Content-SHA256", signedHash)
		request.URL.RawQuery = values.Encode()
		auth.PresignRequest(request, credentials.AccessKeyID, credentials.SecretKey, "us-east-1", "s3", time.Hour, time.Now())
	} else {
		auth.SignRequest(request, credentials.AccessKeyID, credentials.SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, signedHash)
	}
	return request
}

func integrityServe(t *testing.T, env downloadTestEnv, request *http.Request, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: status = %d, want %d", request.Method, request.URL.Path, response.Code, wantStatus)
	}
	return response
}

func integrityPut(t *testing.T, env downloadTestEnv, key, payload string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/downloads/"+key, strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+env.token)
	integrityServe(t, env, request, http.StatusOK)
}

func integrityAssertObject(t *testing.T, env downloadTestEnv, key, want string) {
	t.Helper()
	object, err := env.app.objects.Objects.GetByKey(context.Background(), "downloads", key)
	if err != nil {
		t.Fatal(err)
	}
	file, err := env.app.objects.Storage.Open(context.Background(), object.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != want {
		t.Fatalf("stored object bytes differ: got %d bytes, want %d", len(payload), len(want))
	}
}

func integrityCreateUpload(t *testing.T, env downloadTestEnv, key string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/downloads/"+key+"?uploads", nil)
	request.Header.Set("Authorization", "Bearer "+env.token)
	response := integrityServe(t, env, request, http.StatusOK)
	var upload struct {
		ID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(response.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	if upload.ID == "" {
		t.Fatal("missing upload ID")
	}
	return upload.ID
}
