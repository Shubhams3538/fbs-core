package s3

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/iam"
	"github.com/i-got-this-faa/fbs/internal/metadata"
	"github.com/i-got-this-faa/fbs/internal/storage"
	"modernc.org/sqlite"
)

func TestCanceledPutKeepsMetadataAndBackingFileConsistent(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%v", overwrite), func(t *testing.T) {
			var cancel context.CancelFunc
			var finished chan struct{}
			var yields int
			function := fmt.Sprintf("cancel_put_%d", time.Now().UnixNano())
			// Start real cancellation from SQLite's final write statement, varying
			// scheduling to reach either side of COMMIT without production hooks.
			if err := sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
				if cancel != nil {
					cancelNow, done, n := cancel, finished, yields
					go func() {
						for i := 0; i < n; i++ {
							runtime.Gosched()
						}
						cancelNow()
						close(done)
					}()
				}
				return int64(1), nil
			}); err != nil {
				t.Fatal(err)
			}
			db, err := metadata.Open(filepath.Join(t.TempDir(), "consistency.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if _, err := db.Exec(`INSERT INTO users(id,display_name,access_key_id,secret_hash) VALUES ('owner','owner','owner','owner');
				INSERT INTO buckets(name,owner_id) VALUES ('test-bucket','owner')`); err != nil {
				t.Fatal(err)
			}
			for _, event := range []string{"INSERT", "UPDATE"} {
				if _, err := db.Exec(`CREATE TRIGGER cancel_put_` + event + ` AFTER ` + event + ` ON objects BEGIN SELECT ` + function + `(); END`); err != nil {
					t.Fatal(err)
				}
			}
			disk, err := storage.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			objects := metadata.NewObjectRepository(db)
			handlers := &ObjectHandlers{Buckets: metadata.NewBucketRepository(db), Objects: objects, Storage: disk}
			request := func(ctx context.Context, method, key, body string) *http.Request {
				route := chi.NewRouteContext()
				route.URLParams.Add("bucket", "test-bucket")
				route.URLParams.Add("*", key)
				ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: "owner", Role: iam.RoleAdmin})
				ctx = context.WithValue(ctx, chi.RouteCtxKey, route)
				return httptest.NewRequest(method, "/test-bucket/"+key, strings.NewReader(body)).WithContext(ctx)
			}
			committed, rolledBack := 0, 0
			for i := 0; i < 200; i++ {
				key := fmt.Sprintf("key-%d", i)
				if overwrite {
					seed := httptest.NewRecorder()
					handlers.PutObject(seed, request(context.Background(), http.MethodPut, key, "original"))
					if seed.Code != http.StatusOK {
						t.Fatalf("seed PUT = %d: %s", seed.Code, seed.Body.String())
					}
				}
				ctx, cancelRequest := context.WithCancel(context.Background())
				cancel, finished, yields = cancelRequest, make(chan struct{}), i%80
				put := httptest.NewRecorder()
				handlers.PutObject(put, request(ctx, http.MethodPut, key, "replacement"))
				<-finished
				cancelRequest()
				cancel = nil
				stored, err := objects.GetByKey(context.Background(), "test-bucket", key)
				get := httptest.NewRecorder()
				handlers.GetObject(get, request(context.Background(), http.MethodGet, key, ""))
				if put.Code != http.StatusOK && !overwrite {
					rolledBack++
					if !errors.Is(err, metadata.ErrObjectNotFound) || get.Code != http.StatusNotFound {
						t.Fatalf("iteration %d: failed PUT=%d persisted row: metadata=%v, GET=%d %s", i, put.Code, err, get.Code, get.Body.String())
					}
					continue
				}
				want := "replacement"
				if put.Code != http.StatusOK {
					rolledBack++
					want = "original"
				} else {
					committed++
				}
				if err != nil {
					t.Fatalf("iteration %d: PUT=%d metadata=%v", i, put.Code, err)
				}
				file, err := disk.Open(context.Background(), stored.StoragePath)
				if err != nil {
					t.Fatalf("iteration %d: PUT=%d persisted row has missing file: %v", i, put.Code, err)
				}
				body, readErr := io.ReadAll(file)
				file.Close()
				if readErr != nil || string(body) != want || get.Code != http.StatusOK || get.Body.String() != want {
					t.Fatalf("iteration %d: PUT=%d file=%q read=%v GET=%d %q, want %q", i, put.Code, body, readErr, get.Code, get.Body.String(), want)
				}
			}
			t.Logf("canceled PUT outcomes: committed=%d rolled_back=%d", committed, rolledBack)
		})
	}
}
