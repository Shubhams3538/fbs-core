package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/auth"
)

func TestServerAuth_DevModeBypass(t *testing.T) {
	server := startTestServer(t, "FBS_DEV=true")
	baseURL := server.baseURL

	resp, err := server.client.Get(baseURL + "/_health/auth")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if body["user_id"] != "dev-user" {
		t.Errorf("user_id = %q, want dev-user", body["user_id"])
	}
	if body["role"] != "admin" {
		t.Errorf("role = %q, want admin", body["role"])
	}
	if body["dev_mode"] != true {
		t.Errorf("dev_mode = %v, want true", body["dev_mode"])
	}
}

func TestServerAuth_ProtectedRouteRequiresAuth(t *testing.T) {
	server := startTestServer(t)
	baseURL := server.baseURL

	resp, err := server.client.Get(baseURL + "/_health/auth")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	wwwAuth := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, `Bearer realm="fbs"`) {
		t.Fatalf("expected WWW-Authenticate header, got %q", wwwAuth)
	}
}

func TestServerAuth_BearerToken(t *testing.T) {
	dbPath, issued, _, user := seedServerCredentials(t, "member")

	server := startTestServer(t, "FBS_DB_PATH="+dbPath)
	baseURL := server.baseURL

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/_health/auth", nil)
	req.Header.Set("Authorization", "Bearer "+issued.RawToken)

	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if body["user_id"] != user.ID {
		t.Errorf("user_id = %q, want %q", body["user_id"], user.ID)
	}
	if body["role"] != "member" {
		t.Errorf("role = %q, want member", body["role"])
	}
	if body["dev_mode"] != false {
		t.Errorf("dev_mode = %v, want false", body["dev_mode"])
	}
}

func TestServerAuth_UnsupportedScheme(t *testing.T) {
	server := startTestServer(t)
	baseURL := server.baseURL

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/_health/auth", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	if strings.Contains(resp.Header.Get("WWW-Authenticate"), `Bearer realm="fbs"`) {
		t.Error("unsupported scheme should not trigger WWW-Authenticate")
	}
}

func TestServerAuth_SigV4(t *testing.T) {
	dbPath, _, sigv4, user := seedServerCredentials(t, "admin")

	server := startTestServer(t, "FBS_DB_PATH="+dbPath)
	baseURL := server.baseURL

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/_health/auth", nil)
	req.Host = strings.TrimPrefix(baseURL, "http://")

	auth.SignRequest(req, sigv4.AccessKeyID, sigv4.SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, auth.EmptyStringHash)

	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if body["user_id"] != user.ID {
		t.Errorf("user_id = %q, want %q", body["user_id"], user.ID)
	}
	if body["role"] != "admin" {
		t.Errorf("role = %q, want admin", body["role"])
	}
	if body["dev_mode"] != false {
		t.Errorf("dev_mode = %v, want false", body["dev_mode"])
	}
}

func TestServerAuth_SigV4WrongSignature(t *testing.T) {
	dbPath, _, sigv4, _ := seedServerCredentials(t, "member")

	server := startTestServer(t, "FBS_DB_PATH="+dbPath)
	baseURL := server.baseURL

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/_health/auth", nil)
	req.Host = strings.TrimPrefix(baseURL, "http://")

	auth.SignRequest(req, sigv4.AccessKeyID, "wrong-secret", "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, auth.EmptyStringHash)

	resp, err := server.client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestServerSetupBootstrapCredentialsAccessManagement(t *testing.T) {
	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "test.db")

	server := startTestServer(t, "FBS_DB_PATH="+dbPath)
	baseURL := server.baseURL

	statusResp, err := server.client.Get(baseURL + "/api/setup/status")
	if err != nil {
		t.Fatalf("status request failed: %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", statusResp.StatusCode)
	}
	var statusBody struct {
		BootstrapRequired bool   `json:"bootstrap_required"`
		Region            string `json:"region"`
		ManagementURL     string `json:"management_url"`
		S3URL             string `json:"s3_url"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&statusBody); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !statusBody.BootstrapRequired {
		t.Fatal("bootstrap_required = false, want true")
	}
	if statusBody.Region != "us-east-1" {
		t.Fatalf("region = %q, want us-east-1", statusBody.Region)
	}

	bootstrapResp, err := server.client.Post(baseURL+"/api/setup/bootstrap", "application/json", strings.NewReader(`{"display_name":"Admin User"}`))
	if err != nil {
		t.Fatalf("bootstrap request failed: %v", err)
	}
	defer bootstrapResp.Body.Close()
	bootstrapBodyBytes, err := io.ReadAll(bootstrapResp.Body)
	if err != nil {
		t.Fatalf("read bootstrap response: %v", err)
	}
	if bootstrapResp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap code = %d, want 201; body=%s", bootstrapResp.StatusCode, string(bootstrapBodyBytes))
	}
	if bootstrapResp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", bootstrapResp.Header.Get("Cache-Control"))
	}
	if strings.Contains(string(bootstrapBodyBytes), "secret_hash") {
		t.Fatalf("bootstrap response exposed secret_hash: %s", string(bootstrapBodyBytes))
	}

	var bootstrapBody struct {
		Key struct {
			ID               string `json:"id"`
			DisplayName      string `json:"display_name"`
			AccessKeyID      string `json:"access_key_id"`
			SigV4AccessKeyID string `json:"sigv4_access_key_id"`
			Role             string `json:"role"`
			IsActive         bool   `json:"is_active"`
		} `json:"key"`
		BearerToken string `json:"bearer_token"`
		SigV4       struct {
			AccessKeyID string `json:"access_key_id"`
			SecretKey   string `json:"secret_key"`
		} `json:"sigv4"`
		Region        string `json:"region"`
		ManagementURL string `json:"management_url"`
		S3URL         string `json:"s3_url"`
	}
	if err := json.Unmarshal(bootstrapBodyBytes, &bootstrapBody); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if bootstrapBody.Key.Role != "admin" || !bootstrapBody.Key.IsActive {
		t.Fatalf("bootstrap key = %+v, want active admin", bootstrapBody.Key)
	}

	bearerReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/management/metrics", nil)
	bearerReq.Header.Set("Authorization", "Bearer "+bootstrapBody.BearerToken)
	bearerResp, err := server.client.Do(bearerReq)
	if err != nil {
		t.Fatalf("bearer metrics request failed: %v", err)
	}
	defer bearerResp.Body.Close()
	if bearerResp.StatusCode != http.StatusOK {
		t.Fatalf("bearer metrics code = %d, want 200", bearerResp.StatusCode)
	}

	sigV4Req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/management/metrics", nil)
	sigV4Req.Host = strings.TrimPrefix(baseURL, "http://")
	auth.SignRequest(sigV4Req, bootstrapBody.SigV4.AccessKeyID, bootstrapBody.SigV4.SecretKey, "us-east-1", "s3", []string{"host", "x-amz-content-sha256", "x-amz-date"}, auth.EmptyStringHash)
	sigV4Resp, err := server.client.Do(sigV4Req)
	if err != nil {
		t.Fatalf("sigv4 metrics request failed: %v", err)
	}
	defer sigV4Resp.Body.Close()
	if sigV4Resp.StatusCode != http.StatusOK {
		t.Fatalf("sigv4 metrics code = %d, want 200", sigV4Resp.StatusCode)
	}

	secondResp, err := server.client.Post(baseURL+"/api/setup/bootstrap", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("second bootstrap request failed: %v", err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusConflict {
		t.Fatalf("second bootstrap code = %d, want 409", secondResp.StatusCode)
	}
}

func TestServerStartsWithWhitespacePublicReadSigningSecret(t *testing.T) {
	startTestServer(t, "FBS_PUBLIC_READ_SIGNING_SECRET=   ")
}

func TestServerGracefulShutdown(t *testing.T) {
	server := launchTestServer(t, buildServerBinary(t))
	if err := server.waitReady(); err != nil {
		t.Fatalf("readiness: %v; stdout=%s stderr=%s", err, server.stdout.String(), server.stderr.String())
	}
	server.stop(t)
	if !strings.Contains(server.stdout.String(), "shutting down server") {
		t.Fatalf("missing shutdown log: %s", server.stdout.String())
	}
}

func TestServerProcessIgnoresInheritedStorageAndAuth(t *testing.T) {
	inherited := t.TempDir()
	dataDir := filepath.Join(inherited, "data")
	sentinel := filepath.Join(dataDir, "owned-bucket", "sentinel")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0700); err != nil {
		t.Fatal(err)
	}
	const contents = "test-owned data must survive startup reconciliation"
	if err := os.WriteFile(sentinel, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	// A valid empty DB would let startup reconcile away the orphan sentinel.
	dbPath, _, _, _ := seedServerCredentials(t, "admin")
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FBS_DATA_DIR", dataDir)
	t.Setenv("FBS_DB_PATH", dbPath)
	t.Setenv("FBS_DEV", "true")
	server := startTestServer(t)
	resp, err := server.client.Get(server.baseURL + "/_health/auth")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("inherited dev mode leaked: status=%d", resp.StatusCode)
	}
	server.stop(t)
	for path, want := range map[string]string{sentinel: contents, dbPath: string(before)} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("inherited file %s changed: read error=%v, contents equal=%v", path, err, string(got) == want)
		}
	}
	for _, name := range []string{"fbs.db", "data"} {
		if _, err := os.Stat(filepath.Join(server.cmd.Dir, name)); err != nil {
			t.Errorf("server did not create isolated %s: %v", name, err)
		}
	}
}
