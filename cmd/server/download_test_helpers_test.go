package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/config"
	httpapi "github.com/i-got-this-faa/fbs/internal/http"
	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/storage"
)

type downloadTestEnv struct {
	cfg     config.Config
	app     *app
	handler http.Handler
	token   string
	paths   map[string]string
	body    []byte
}

func newDownloadTestEnv(t *testing.T) downloadTestEnv {
	t.Helper()
	cfg := config.Default()
	cfg.WriteTimeout = 500 * time.Millisecond
	cfg.PublicReadSigningSecret = "12345678901234567890123456789012"
	db, err := metadata.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	disk, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := newApp(cfg, db, disk, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	token, _, user, err := auth.CreateBearerToken(ctx, application.objects.Users, "Download Test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	const bucket, key = "downloads", "video.mp4"
	now := time.Now().UTC()
	if err := application.objects.Buckets.Create(ctx, &metadata.Bucket{
		Name: bucket, OwnerID: user.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4*1024*1024)
	random := rand.NewChaCha8([32]byte{42})
	if _, err := random.Read(body); err != nil {
		t.Fatal(err)
	}
	storagePath, size, err := disk.Write(ctx, bucket, key, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.objects.Objects.Create(ctx, &metadata.Object{
		ID: "download-object", BucketName: bucket, Key: key,
		StoragePath: storagePath, Size: size, ETag: "download-etag",
		ContentType: "video/mp4", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := application.objects.ShareLinks.Create(ctx, &metadata.ShareLink{
		Code: "download", BucketName: bucket, ObjectKey: key,
		CreatedBy: user.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	publicPath := "/public/" + bucket + "/" + key
	expires := now.Add(time.Hour)
	signature := application.objects.PublicReadSigner.SignPath(publicPath, expires, "")
	return downloadTestEnv{
		cfg: cfg, app: application, handler: newRouter(cfg, logger, application),
		token: token.RawToken, body: body,
		paths: map[string]string{
			"authenticated": "/" + bucket + "/" + key,
			"signed":        publicPath + "?expires=" + strconv.FormatInt(expires.Unix(), 10) + "&signature=" + signature,
			"share_link":    "/s/download/video.mp4",
		},
	}
}

func (env downloadTestEnv) startServer(t *testing.T) (*httptest.Server, <-chan time.Duration) {
	t.Helper()
	done := make(chan time.Duration, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		env.handler.ServeHTTP(w, r)
		done <- time.Since(started)
	}))
	server.Config.WriteTimeout = env.cfg.WriteTimeout
	server.Config.ReadTimeout = env.cfg.ReadTimeout
	server.Config.IdleTimeout = env.cfg.IdleTimeout
	server.Config.ConnContext = httpapi.WriteTimeoutConnContext
	server.Listener = httpapi.NewWriteTimeoutListener(server.Listener)
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			underlying := conn.(interface{ NetConn() net.Conn }).NetConn()
			if err := underlying.(*net.TCPConn).SetWriteBuffer(16 * 1024); err != nil {
				t.Errorf("set socket write buffer: %v", err)
			}
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, done
}

func (env downloadTestEnv) request(t *testing.T, server *httptest.Server, path, byteRange string) (*http.Response, *net.TCPConn) {
	t.Helper()
	conn, err := net.DialTCP("tcp", nil, server.Listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadBuffer(32 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path == env.paths["authenticated"] {
		req.Header.Set("Authorization", "Bearer "+env.token)
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, conn
}
