package s3

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net/http"
	"strings"

	"github.com/i-got-this-faa/fbs/internal/auth"
)

type checksumPipeline struct {
	md5Hash hash.Hash
	checks  []checksumCheck
	writer  io.Writer
}

type checksumCheck struct {
	name     string
	expected []byte
	sum      func() []byte
}

func newChecksumPipeline(r *http.Request) (*checksumPipeline, error) {
	header := r.Header
	md5Hash := md5.New()
	writers := []io.Writer{md5Hash}

	checks := []checksumCheck{}
	if value := header.Get("Content-MD5"); value != "" {
		expected, err := decodeChecksum(value)
		if err != nil || len(expected) != md5.Size {
			return nil, fmt.Errorf("%s: %w", "Content-MD5", errInvalidDigest)
		}
		checks = append(checks, checksumCheck{
			name:     "Content-MD5",
			expected: expected,
			sum: func() []byte {
				return md5Hash.Sum(nil)
			},
		})
	}

	principal, _ := auth.PrincipalFromContext(r.Context())
	var payloadHash hash.Hash
	for _, value := range []string{strings.TrimSpace(header.Get("X-Amz-Content-SHA256")), principal.SignedPayloadHash} {
		if value == "" || value == unsignedPayloadHash {
			continue
		}
		expected, err := hex.DecodeString(value)
		if err != nil || len(expected) != sha256.Size {
			return nil, fmt.Errorf("%s: %w", "X-Amz-Content-SHA256", errInvalidDigest)
		}
		if payloadHash == nil {
			payloadHash = sha256.New()
			writers = append(writers, payloadHash)
		}
		checks = append(checks, checksumCheck{
			name:     "X-Amz-Content-SHA256",
			expected: expected,
			sum: func() []byte {
				return payloadHash.Sum(nil)
			},
		})
	}

	addHashCheck := func(headerName string, h hash.Hash, size int) error {
		value := header.Get(headerName)
		if value == "" {
			return nil
		}
		expected, err := decodeChecksum(value)
		if err != nil || len(expected) != size {
			return fmt.Errorf("%s: %w", headerName, errInvalidChecksum)
		}
		writers = append(writers, h)
		checks = append(checks, checksumCheck{
			name:     headerName,
			expected: expected,
			sum: func() []byte {
				return h.Sum(nil)
			},
		})
		return nil
	}

	if err := addHashCheck("x-amz-checksum-sha1", sha1.New(), sha1.Size); err != nil {
		return nil, err
	}
	if err := addHashCheck("x-amz-checksum-sha256", sha256.New(), sha256.Size); err != nil {
		return nil, err
	}

	if err := addCRC32Check(header, "x-amz-checksum-crc32", crc32.IEEETable, &writers, &checks); err != nil {
		return nil, err
	}
	if err := addCRC32Check(header, "x-amz-checksum-crc32c", crc32.MakeTable(crc32.Castagnoli), &writers, &checks); err != nil {
		return nil, err
	}
	if err := addCRC64NVME(header, &writers, &checks); err != nil {
		return nil, err
	}

	return &checksumPipeline{
		md5Hash: md5Hash,
		checks:  checks,
		writer:  io.MultiWriter(writers...),
	}, nil
}

func addCRC32Check(header http.Header, headerName string, table *crc32.Table, writers *[]io.Writer, checks *[]checksumCheck) error {
	value := header.Get(headerName)
	if value == "" {
		return nil
	}

	expected, err := decodeChecksum(value)
	if err != nil || len(expected) != crc32.Size {
		return fmt.Errorf("%s: %w", headerName, errInvalidChecksum)
	}

	h := crc32.New(table)
	*writers = append(*writers, h)
	*checks = append(*checks, checksumCheck{
		name:     headerName,
		expected: expected,
		sum: func() []byte {
			sum := make([]byte, crc32.Size)
			binary.BigEndian.PutUint32(sum, h.Sum32())
			return sum
		},
	})

	return nil
}

// Go's CRC64 implementation expects the reflected CRC64-NVME polynomial.
// Reference: https://github.com/awslabs/aws-checksums/blob/main/tests/crc64_test.c
var crc64NVMeTable = crc64.MakeTable(0x9a6c9329ac4bc9b5)

func addCRC64NVME(header http.Header, writers *[]io.Writer, checks *[]checksumCheck) error {
	headerName := "x-amz-checksum-crc64nvme"
	value := strings.TrimSpace(header.Get(headerName))
	if value == "" {
		return nil
	}
	expected, err := decodeChecksum(value)
	if err != nil || len(expected) != crc64.Size {
		return fmt.Errorf("%s: %w", headerName, errInvalidChecksum)
	}
	h := crc64.New(crc64NVMeTable)
	*writers = append(*writers, h)
	*checks = append(*checks, checksumCheck{
		name:     headerName,
		expected: expected,
		sum: func() []byte {
			return h.Sum(nil)
		},
	})
	return nil
}

func (p *checksumPipeline) Reader(r io.Reader) io.Reader {
	return io.TeeReader(r, p.writer)
}

func (p *checksumPipeline) ETag() string {
	return hex.EncodeToString(p.md5Hash.Sum(nil))
}

// Checksums returns a map of all computed checksums keyed by header name.
// Only includes checksums that were explicitly requested.
func (p *checksumPipeline) Checksums() map[string]string {
	cksum := make(map[string]string)
	for _, c := range p.checks {
		cksum[c.name] = base64.StdEncoding.EncodeToString(c.sum())
	}
	return cksum
}

func (p *checksumPipeline) Validate() error {
	for _, check := range p.checks {
		if !bytes.Equal(check.expected, check.sum()) {
			return fmt.Errorf("%s: %w", check.name, errChecksumMismatch)
		}
	}
	return nil
}

func decodeChecksum(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}

type checksumError string

func (e checksumError) Error() string {
	return string(e)
}

const (
	errInvalidDigest    checksumError = "invalid digest"
	errInvalidChecksum  checksumError = "invalid checksum"
	errChecksumMismatch checksumError = "checksum mismatch"
)
