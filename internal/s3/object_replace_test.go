package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/i-got-this-faa/fbs/internal/metadata"
)

func TestObjectWritesEnforceDestinationPreconditions(t *testing.T) {
	for _, copyObject := range []bool{false, true} {
		for _, test := range []struct {
			name, header, value string
			exists              bool
			status              int
		}{
			{"create absent", "If-None-Match", "*", false, 200},
			{"keep existing", "If-None-Match", "*", true, 412},
			{"wrong match", "If-Match", `"different"`, true, 412},
			{"missing match", "If-Match", `"different"`, false, 404},
			{"matching etag", "If-Match", quotedMD5("original"), true, 200},
			{"existing wildcard", "If-Match", "*", true, 200},
			{"matching none", "If-None-Match", quotedMD5("original"), true, 412},
		} {
			t.Run(fmt.Sprintf("copy=%t/%s", copyObject, test.name), func(t *testing.T) {
				env := newObjectTestEnv(t)
				ctx := context.Background()
				env.mustPut(t, "source", "replacement")
				var originalPath string
				if test.exists {
					env.mustPut(t, "destination", "original")
					obj, err := env.objects.GetByKey(ctx, env.bucket, "destination")
					if err != nil {
						t.Fatal(err)
					}
					originalPath = obj.StoragePath
				}
				headers := map[string]string{test.header: test.value}
				if copyObject {
					headers["x-amz-copy-source"] = "/" + env.bucket + "/source"
				}
				response := env.do(t, http.MethodPut, "/"+env.bucket+"/destination", "replacement", headers)
				if response.Code != test.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
				}
				get := env.do(t, http.MethodGet, "/"+env.bucket+"/destination", "", nil)
				want := "replacement"
				if test.status != 200 {
					want = "original"
				}
				if !test.exists && test.status != 200 {
					if get.Code != 404 {
						t.Fatalf("failed write created object: %d", get.Code)
					}
				} else if get.Code != 200 || get.Body.String() != want {
					t.Fatalf("destination=%d %q want=%q", get.Code, get.Body.String(), want)
				}
				if originalPath != "" {
					file, err := env.storage.Open(ctx, originalPath)
					if test.status == 200 {
						if err == nil {
							file.Close()
							t.Fatal("replaced backing file retained")
						}
					} else if err != nil {
						t.Fatalf("failed write removed old backing file: %v", err)
					} else {
						file.Close()
					}
				}
			})
		}
	}
}

func TestConcurrentConditionalWritesHaveExactlyOneWinner(t *testing.T) {
	env := newObjectTestEnv(t)
	// The repository is the shared commit boundary for PUT, CopyObject, and
	// multipart completion. Exercise it through separate real DB connections.
	const writers = 12
	results := make(chan error, writers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range writers {
		wg.Go(func() {
			<-start
			obj := &metadata.Object{ID: fmt.Sprint(i), BucketName: env.bucket, Key: "once", ETag: fmt.Sprint(i), StoragePath: fmt.Sprint(i)}
			_, err := env.objects.Replace(context.Background(), obj, "", "*")
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, metadata.ErrPreconditionFailed) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful conditional writes=%d want=1", winners)
	}
}
