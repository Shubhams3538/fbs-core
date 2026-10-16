package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestObjectDownloadsOutliveWriteTimeout(t *testing.T) {
	env := newDownloadTestEnv(t)
	for name, path := range env.paths {
		for _, ranged := range []bool{false, true} {
			caseName := name + "/full"
			if ranged {
				caseName = name + "/range"
			}
			t.Run(caseName, func(t *testing.T) {
				t.Parallel()
				server, done := env.startServer(t)
				byteRange, wantStatus := "", http.StatusOK
				want := env.body
				if ranged {
					byteRange, wantStatus = "bytes=1048576-3145727", http.StatusPartialContent
					want = env.body[1048576:3145728]
				}
				resp, _ := env.request(t, server, path, byteRange)
				if resp.StatusCode != wantStatus {
					t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
				}
				if resp.ContentLength != int64(len(want)) {
					t.Fatalf("Content-Length = %d, want %d", resp.ContentLength, len(want))
				}
				if ranged && resp.Header.Get("Content-Range") != "bytes 1048576-3145727/4194304" {
					t.Fatalf("Content-Range = %q", resp.Header.Get("Content-Range"))
				}
				if resp.Header.Get("Content-Type") != "video/mp4" || resp.Header.Get("ETag") != `"download-etag"` {
					t.Fatalf("unexpected object headers: %v", resp.Header)
				}
				var got bytes.Buffer
				buf := make([]byte, 32*1024)
				for {
					n, err := resp.Body.Read(buf)
					got.Write(buf[:n])
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("download truncated at %d/%d bytes: %v", got.Len(), len(want), err)
					}
					time.Sleep(25 * time.Millisecond)
				}
				if !bytes.Equal(got.Bytes(), want) {
					t.Fatalf("download bytes differ: got %d bytes, want %d", got.Len(), len(want))
				}
				select {
				case duration := <-done:
					if duration < 2*env.cfg.WriteTimeout {
						t.Fatalf("server streamed for %s; test needs backpressure beyond %s", duration, 2*env.cfg.WriteTimeout)
					}
				case <-time.After(time.Second):
					t.Fatal("download handler did not finish")
				}
			})
		}
	}
}

func TestStalledObjectDownloadsTimeOut(t *testing.T) {
	env := newDownloadTestEnv(t)
	for name, path := range env.paths {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, done := env.startServer(t)
			resp, _ := env.request(t, server, path, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			// Stop reading so both socket buffers fill and the server blocks.
			select {
			case duration := <-done:
				if duration < env.cfg.WriteTimeout/2 {
					t.Fatalf("handler finished before the write timeout: %s", duration)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stalled reader kept the download handler alive")
			}
			n, err := io.Copy(io.Discard, resp.Body)
			if err == nil || n >= int64(len(env.body)) {
				t.Fatalf("stalled download: bytes = %d, error = %v; want a truncated response", n, err)
			}
		})
	}
}

func TestObjectDownloadWithoutRenewalHitsServerDeadline(t *testing.T) {
	env := newDownloadTestEnv(t)
	// Negative control: retain the original server-wide deadline to prove
	// these socket buffers actually backpressure a complete object transfer.
	env.app.objects.WriteTimeout = 0
	server, done := env.startServer(t)
	resp, _ := env.request(t, server, env.paths["authenticated"], "")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original server deadline did not terminate the transfer")
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil || n >= int64(len(env.body)) {
		t.Fatalf("original deadline: bytes = %d, error = %v; expected truncation", n, err)
	}
}

func TestObjectDownloadDoesNotChangeNextResponseDeadline(t *testing.T) {
	env := newDownloadTestEnv(t)
	original := env.handler
	env.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(3 * env.cfg.WriteTimeout)
			_, _ = io.WriteString(w, "ordinary response")
			return
		}
		original.ServeHTTP(w, r)
	})
	server, done := env.startServer(t)
	resp, conn := env.request(t, server, env.paths["authenticated"], "bytes=0-15")
	if body, err := io.ReadAll(resp.Body); err != nil || !bytes.Equal(body, env.body[:16]) {
		t.Fatalf("first response: body = %x, error = %v", body, err)
	}
	<-done
	req, err := http.NewRequest(http.MethodGet, server.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.ReadResponse(bufio.NewReader(conn), req); err == nil {
		resp.Body.Close()
		t.Fatal("ordinary response outlived its total deadline after a download")
	}
}
