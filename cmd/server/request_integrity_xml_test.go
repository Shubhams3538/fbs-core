package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func TestSignedBucketControlBodyIntegrity(t *testing.T) {
	const document = `<CreateBucketConfiguration><LocationConstraint>us-east-1</LocationConstraint></CreateBucketConfiguration>`
	for _, query := range []bool{false, true} {
		for _, payload := range []string{document, document + " ", ""} {
			env := newDownloadTestEnv(t)
			credentials := integritySigner(t, env)
			request := integrityRequest(http.MethodPut, "/integrity-bucket", payload, integrityHash(document), credentials, query)
			if payload == "" {
				request.Body = nil
			}
			wantStatus := http.StatusBadRequest
			if payload == document {
				wantStatus = http.StatusOK
			}
			integrityServe(t, env, request, wantStatus)
			_, err := env.app.objects.Buckets.GetByName(context.Background(), "integrity-bucket")
			if wantStatus == http.StatusOK && err != nil {
				t.Fatal(err)
			}
			if wantStatus != http.StatusOK && !errors.Is(err, metadata.ErrBucketNotFound) {
				t.Fatalf("invalid signed body created bucket: %v", err)
			}
		}
	}
}

type integrityClaimRecorder struct {
	metadata.MultipartUploadRepository
	claims int
}

func (recorder *integrityClaimRecorder) ClaimUpload(ctx context.Context, uploadID string, status metadata.MultipartUploadStatus) error {
	recorder.claims++
	return recorder.MultipartUploadRepository.ClaimUpload(ctx, uploadID, status)
}

func TestSignedMultipartCompletionBodyIntegrity(t *testing.T) {
	for _, query := range []bool{false, true} {
		env := newDownloadTestEnv(t)
		credentials := integritySigner(t, env)
		const key, preserved, contents = "complete-integrity.bin", "original object", "uploaded part"
		integrityPut(t, env, key, preserved)
		uploadID := integrityCreateUpload(t, env, key)
		path := "/downloads/" + key + "?uploadId=" + url.QueryEscape(uploadID)
		partRequest := integrityRequest(http.MethodPut, path+"&partNumber=1", contents, integrityHash(contents), credentials, query)
		part := integrityServe(t, env, partRequest, http.StatusOK)
		document := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, part.Header().Get("ETag"))
		claims := &integrityClaimRecorder{MultipartUploadRepository: env.app.objects.MultipartUploads}
		env.app.objects.MultipartUploads = claims
		changed := integrityRequest(http.MethodPost, path, document+" ", integrityHash(document), credentials, query)
		integrityServe(t, env, changed, http.StatusBadRequest)
		if claims.claims != 0 {
			t.Fatalf("invalid signed body claimed upload %d times", claims.claims)
		}
		integrityAssertObject(t, env, key, preserved)
		upload, err := env.app.multipartUploads.GetByID(context.Background(), uploadID)
		if err != nil || upload.Status != metadata.MultipartUploadStatusActive {
			t.Fatalf("invalid signed body changed upload status: upload=%+v error=%v", upload, err)
		}
		parts, err := env.app.multipartUploads.ListParts(context.Background(), uploadID)
		if err != nil || len(parts) != 1 {
			t.Fatalf("invalid signed body changed parts: count=%d error=%v", len(parts), err)
		}
		valid := integrityRequest(http.MethodPost, path, document, integrityHash(document), credentials, query)
		integrityServe(t, env, valid, http.StatusOK)
		integrityAssertObject(t, env, key, contents)
		if _, err := env.app.multipartUploads.GetByID(context.Background(), uploadID); !errors.Is(err, metadata.ErrMultipartUploadNotFound) {
			t.Fatalf("valid completion retained upload: %v", err)
		}
	}
}

func TestSignedDeleteControlBodyIntegrity(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		env := newDownloadTestEnv(t)
		credentials := integritySigner(t, env)
		const original = `<Delete><Object><Key>first.txt</Key></Object></Delete>`
		const changed = `<Delete><Object><Key>second.txt</Key></Object></Delete>`
		integrityPut(t, env, "first.txt", "first object")
		integrityPut(t, env, "second.txt", "second object")
		request := integrityRequest(method, "/downloads?delete", changed, integrityHash(original), credentials, false)
		integrityServe(t, env, request, http.StatusBadRequest)
		integrityAssertObject(t, env, "first.txt", "first object")
		integrityAssertObject(t, env, "second.txt", "second object")
		valid := integrityRequest(method, "/downloads?delete", original, integrityHash(original), credentials, false)
		integrityServe(t, env, valid, http.StatusOK)
		if _, err := env.app.objects.Objects.GetByKey(context.Background(), "downloads", "first.txt"); !errors.Is(err, metadata.ErrObjectNotFound) {
			t.Fatalf("valid delete did not remove first object: %v", err)
		}
		integrityAssertObject(t, env, "second.txt", "second object")
	}
}
