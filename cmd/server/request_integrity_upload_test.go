package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSignedUploadBodyIntegrity(t *testing.T) {
	const original, changed, preserved = "approved", "changed!", "preserved object or part"
	for _, part := range []bool{false, true} {
		kind := "object"
		if part {
			kind = "part"
		}
		for _, test := range []struct {
			name       string
			query      bool
			payload    string
			header     string
			signedHash string
			status     int
		}{
			{"header valid", false, original, "", integrityHash(original), http.StatusOK},
			{"header changed", false, changed, "", integrityHash(original), http.StatusBadRequest},
			{"query valid", true, original, "", integrityHash(original), http.StatusOK},
			{"query changed", true, changed, "", integrityHash(original), http.StatusBadRequest},
			{"query conflicting header", true, changed, integrityHash(changed), integrityHash(original), http.StatusBadRequest},
			{"query unsigned header", true, changed, "UNSIGNED-PAYLOAD", integrityHash(original), http.StatusBadRequest},
			{"query conflicting digest on original", true, original, integrityHash(changed), integrityHash(original), http.StatusBadRequest},
			{"query unsigned payload", true, changed, "", "UNSIGNED-PAYLOAD", http.StatusOK},
			{"header unsigned payload", false, changed, "", "UNSIGNED-PAYLOAD", http.StatusOK},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				env := newDownloadTestEnv(t)
				credentials := integritySigner(t, env)
				const key = "integrity.bin"
				path := "/downloads/" + key
				uploadID := ""
				if part {
					uploadID = integrityCreateUpload(t, env, key)
					path += "?partNumber=1&uploadId=" + url.QueryEscape(uploadID)
					seed := integrityRequest(http.MethodPut, path, preserved, integrityHash(preserved), credentials, false)
					integrityServe(t, env, seed, http.StatusOK)
				} else {
					integrityPut(t, env, key, preserved)
				}
				request := integrityRequest(http.MethodPut, path, test.payload, test.signedHash, credentials, test.query)
				request.ContentLength = -1
				if test.header != "" {
					request.Header.Set("X-Amz-Content-SHA256", test.header)
				}
				integrityServe(t, env, request, test.status)
				want := preserved
				if test.status == http.StatusOK {
					want = test.payload
				}
				if !part {
					integrityAssertObject(t, env, key, want)
					return
				}
				parts, err := env.app.multipartUploads.ListParts(context.Background(), uploadID)
				if err != nil || len(parts) != 1 {
					t.Fatalf("persisted parts = %d, error = %v", len(parts), err)
				}
				file, err := env.app.objects.Storage.Open(context.Background(), parts[0].StoragePath)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				payload, err := io.ReadAll(file)
				if err != nil || string(payload) != want {
					t.Fatalf("persisted part bytes = %q, error = %v; want %q", payload, err, want)
				}
			})
		}
	}
}

func TestSignedUploadsKeepStreamingBeyondControlBodyLimits(t *testing.T) {
	payload := strings.Repeat("streaming upload\n", 200000)
	for _, part := range []bool{false, true} {
		env := newDownloadTestEnv(t)
		credentials := integritySigner(t, env)
		const key = "stream-integrity.bin"
		path := "/downloads/" + key
		if part {
			uploadID := integrityCreateUpload(t, env, key)
			path += "?partNumber=1&uploadId=" + url.QueryEscape(uploadID)
		}
		request := integrityRequest(http.MethodPut, path, payload, integrityHash(payload), credentials, true)
		request.ContentLength = -1
		integrityServe(t, env, request, http.StatusOK)
		if !part {
			integrityAssertObject(t, env, key, payload)
		}
	}
}
