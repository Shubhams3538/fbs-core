package s3

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

type countedBody struct {
	io.Reader
	read int
}

func (body *countedBody) Read(buffer []byte) (int, error) {
	n, err := body.Reader.Read(buffer)
	body.read += n
	return n, err
}

func TestCreateBucketRejectsOversizedConfiguration(t *testing.T) {
	for _, unknownLength := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown_length=%t", unknownLength), func(t *testing.T) {
			env := newObjectTestEnv(t)
			payload := strings.Repeat(" ", 4<<10) + createBucketConfigXML("us-east-1")
			body := &countedBody{Reader: strings.NewReader(payload)}
			req := httptest.NewRequest(http.MethodPut, "/oversized-bucket", body)
			if !unknownLength {
				req.ContentLength = int64(len(payload))
			} else {
				req.ContentLength = -1
			}
			response := httptest.NewRecorder()
			env.router.ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			if body.read > 4<<10+1 {
				t.Fatalf("read %d bytes beyond configuration limit", body.read)
			}
			if _, err := env.buckets.GetByName(context.Background(), "oversized-bucket"); !errors.Is(err, metadata.ErrBucketNotFound) {
				t.Fatalf("oversized request created bucket: %v", err)
			}
		})
	}
}

func TestCompleteMultipartRejectsOversizedBodyAndPreservesRetry(t *testing.T) {
	env := newObjectTestEnv(t)
	const key, contents = "bounded.bin", "keep uploaded parts"
	uploadID := env.mustCreateMultipartUpload(t, key)
	path := fmt.Sprintf("/%s/%s?uploadId=%s", env.bucket, key, uploadID)
	part := env.do(t, http.MethodPut, path+"&partNumber=1", contents, nil)
	if part.Code != http.StatusOK {
		t.Fatalf("upload part: %d: %s", part.Code, part.Body.String())
	}
	valid := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, part.Header().Get("ETag"))
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat(" ", 2<<20)+valid))
	request.ContentLength = -1
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	upload, err := env.multipartUploads.GetByID(context.Background(), uploadID)
	if err != nil || upload.Status != metadata.MultipartUploadStatusActive {
		t.Fatalf("upload must remain active: upload=%+v error=%v", upload, err)
	}
	parts, err := env.multipartUploads.ListParts(context.Background(), uploadID)
	if err != nil || len(parts) != 1 {
		t.Fatalf("parts must remain: %d, error=%v", len(parts), err)
	}
	if response := env.do(t, http.MethodPost, path, valid, nil); response.Code != http.StatusOK {
		t.Fatalf("retry failed: %d: %s", response.Code, response.Body.String())
	}
	get := env.do(t, http.MethodGet, "/"+env.bucket+"/"+key, "", nil)
	if get.Code != http.StatusOK || get.Body.String() != contents {
		t.Fatalf("retry object: %d, %q", get.Code, get.Body.String())
	}
}

func TestDeleteObjectsRejectsTruncatedBodyBeforeDeletion(t *testing.T) {
	env := newObjectTestEnv(t)
	env.mustPut(t, "keep.txt", "keep me")
	const document = `<Delete><Object><Key>keep.txt</Key></Object></Delete>`
	prefix := document + strings.Repeat(" ", (2<<20)-len(document))
	digest := md5.Sum([]byte(prefix))
	request := httptest.NewRequest(http.MethodPost, "/"+env.bucket+"?delete", strings.NewReader(prefix+"x"))
	request.ContentLength = -1
	request.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(digest[:]))
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if get := env.do(t, http.MethodGet, "/"+env.bucket+"/keep.txt", "", nil); get.Code != http.StatusOK || get.Body.String() != "keep me" {
		t.Fatalf("invalid request deleted object: status=%d body=%q", get.Code, get.Body.String())
	}
}
