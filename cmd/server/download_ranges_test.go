package main

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"
	"time"
)

func TestMultipartObjectDownloadsOutliveWriteTimeout(t *testing.T) {
	env := newDownloadTestEnv(t)
	// Allow TCP receive-window update pauses while keeping the whole transfer
	// much longer than the deadline. A very short timeout can expire even as
	// the application drains bytes already buffered on the client.
	env.cfg.WriteTimeout = 2 * time.Second
	env.app.objects.WriteTimeout = env.cfg.WriteTimeout
	for name, path := range env.paths {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, done := env.startServer(t)
			resp, _ := env.request(t, server, path, "bytes=0-1048575,3145728-4194303")
			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("status = %d, want 206", resp.StatusCode)
			}
			_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(resp.Body, params["boundary"])
			for i, want := range [][]byte{env.body[:1048576], env.body[3145728:]} {
				part, err := reader.NextPart()
				if err != nil {
					t.Fatal(err)
				}
				var got bytes.Buffer
				buf := make([]byte, 4*1024)
				for {
					n, err := part.Read(buf)
					got.Write(buf[:n])
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("part %d truncated at %d bytes: %v", i, got.Len(), err)
					}
					time.Sleep(20 * time.Millisecond)
				}
				if !bytes.Equal(got.Bytes(), want) {
					t.Fatalf("part %d bytes differ", i)
				}
			}
			if _, err := reader.NextPart(); err != io.EOF {
				t.Fatalf("final multipart boundary: %v", err)
			}
			if duration := <-done; duration <= 2*env.cfg.WriteTimeout {
				t.Fatalf("server streamed for %s; need backpressure beyond %s", duration, 2*env.cfg.WriteTimeout)
			}
		})
	}
}
