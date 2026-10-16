package s3

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"testing"
)

// Known answers come from AWS's independently maintained CRC64-NVME suite:
// https://github.com/awslabs/aws-checksums/blob/main/tests/crc64_test.c
func TestCRC64NVMEKnownAnswers(t *testing.T) {
	ascending := make([]byte, 32)
	for i := range ascending {
		ascending[i] = byte(i)
	}
	for _, test := range []struct {
		name     string
		input    []byte
		checksum uint64
	}{
		{"digits", []byte("123456789"), 0xae8b14860a799888},
		{"zeros", make([]byte, 32), 0xcf3473434d4ecf3b},
		{"ascending", ascending, 0xb9d9d4a8492cbd7f},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := crc64.Checksum(test.input, crc64NVMeTable); got != test.checksum {
				t.Fatalf("CRC64-NVME = %016x, want %016x", got, test.checksum)
			}
		})
	}
}

func TestCRC64NVMEObjectAndPartValidation(t *testing.T) {
	const payload, correct, obsolete = "123456789", "rosUhgp5mIg=", "924Lb6jQXg0="
	for _, part := range []bool{false, true} {
		t.Run(fmt.Sprintf("part=%t", part), func(t *testing.T) {
			env := newObjectTestEnv(t)
			path := "/" + env.bucket + "/crc"
			uploadID := ""
			if part {
				uploadID = env.mustCreateMultipartUpload(t, "crc")
				path += "?partNumber=1&uploadId=" + uploadID
			} else {
				env.mustPut(t, "crc", "preserved")
			}
			for _, checksum := range []string{obsolete, base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint64(nil, 1))} {
				response := env.do(t, http.MethodPut, path, payload, map[string]string{"x-amz-checksum-crc64nvme": checksum})
				if response.Code != http.StatusBadRequest {
					t.Fatalf("invalid checksum status=%d", response.Code)
				}
				assertS3ErrorCode(t, response.Body.Bytes(), codeBadDigest)
			}
			if part {
				parts, err := env.multipartUploads.ListParts(context.Background(), uploadID)
				if err != nil || len(parts) != 0 {
					t.Fatalf("invalid parts persisted: %d, %v", len(parts), err)
				}
			} else {
				response := env.do(t, http.MethodGet, path, "", nil)
				if response.Body.String() != "preserved" {
					t.Fatal("invalid checksum replaced object")
				}
			}
			response := env.do(t, http.MethodPut, path, payload, map[string]string{"x-amz-checksum-crc64nvme": correct})
			if response.Code != http.StatusOK || response.Header().Get("x-amz-checksum-crc64nvme") != correct {
				t.Fatalf("valid checksum: status=%d, header=%q", response.Code, response.Header().Get("x-amz-checksum-crc64nvme"))
			}
			if part {
				parts, err := env.multipartUploads.ListParts(context.Background(), uploadID)
				if err != nil || len(parts) != 1 || parts[0].ChecksumCRC64NVME != correct {
					t.Fatalf("persisted part checksum: %+v, %v", parts, err)
				}
				file, err := env.storage.Open(context.Background(), parts[0].StoragePath)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				data, err := io.ReadAll(file)
				if err != nil || string(data) != payload {
					t.Fatalf("part bytes=%q error=%v", data, err)
				}
			} else {
				obj, err := env.objects.GetByKey(context.Background(), env.bucket, "crc")
				if err != nil || obj.ChecksumCRC64NVME != correct {
					t.Fatalf("persisted checksum: %+v, %v", obj, err)
				}
				response := env.do(t, http.MethodGet, path, "", nil)
				if response.Body.String() != payload {
					t.Fatal("valid object bytes differ")
				}
			}
		})
	}
}
