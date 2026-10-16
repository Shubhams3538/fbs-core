package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/i-got-this-faa/fbs/internal/config"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), testLogger(), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("expected JSON content type, got %q", got)
	}

	if body := strings.TrimSpace(recorder.Body.String()); body != `{"status":"ok"}` {
		t.Fatalf("expected health response body, got %q", body)
	}
}

func TestNotFound(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), testLogger(), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/missing", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), testLogger(), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/healthz", nil)
	request.Header.Set("Origin", "https://dashboard.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://dashboard.example.com" {
		t.Fatalf("expected CORS allow origin header, got %q", got)
	}

	if got := recorder.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodGet) {
		t.Fatalf("expected allow methods to contain %q, got %q", http.MethodGet, got)
	}
}

func TestCORSExposesContentDisposition(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), testLogger(), func(r chi.Router) {
		r.Get("/download", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="archive.tar.gz"`)
		})
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/download", nil)
	request.Header.Set("Origin", "https://dashboard.example.com")

	router.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Content-Disposition"); got != `attachment; filename="archive.tar.gz"` {
		t.Fatalf("Content-Disposition = %q, want attachment filename", got)
	}
	exposedHeaders := recorder.Header().Get("Access-Control-Expose-Headers")
	for _, header := range strings.Split(exposedHeaders, ",") {
		if strings.EqualFold(strings.TrimSpace(header), "Content-Disposition") {
			return
		}
	}
	t.Fatalf("exposed headers = %q, want Content-Disposition", exposedHeaders)
}

func TestRouterPreservesReaderFrom(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), testLogger(), func(r chi.Router) {
		r.Get("/reader-from", func(w http.ResponseWriter, _ *http.Request) {
			if _, ok := w.(io.ReaderFrom); !ok {
				http.Error(w, "missing ReaderFrom", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/reader-from", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	router := NewRouter(testConfig(), testLogger(), func(r chi.Router) {
		r.Get("/panic", func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, recorder.Code)
	}

	if body := strings.TrimSpace(recorder.Body.String()); body != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("expected recovery response body, got %q", body)
	}
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.CORSAllowedOrigins = []string{"https://dashboard.example.com"}
	return cfg
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
