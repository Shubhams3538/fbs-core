package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/auth"
	"github.com/i-got-this-faa/fbs/internal/iam"
	"github.com/i-got-this-faa/fbs/internal/metadata"
)

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func buildServerBinary(t *testing.T, tags ...string) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "fbs-server")
	args := []string{"build", "-o", binPath}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	cmd := exec.Command("go", append(args, ".")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	return binPath
}

func findFreePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// Only explicit test overrides may supply FBS settings to a child server.
func serverTestEnv(overrides ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "FBS_") {
			env = append(env, entry)
		}
	}
	return append(env, overrides...)
}

type testServerProcess struct {
	cmd            *exec.Cmd
	baseURL        string
	client         *http.Client
	stdout, stderr synchronizedBuffer
	done           chan struct{}
	waitErr        error // Published by closing done; only the Wait goroutine writes it.
	stopOnce       sync.Once
}

func launchTestServer(t *testing.T, binary string, extraEnv ...string) *testServerProcess {
	t.Helper()
	addr := findFreePort(t)
	process := &testServerProcess{
		cmd:     exec.Command(binary),
		baseURL: "http://" + addr,
		client:  &http.Client{Timeout: 2 * time.Second},
		done:    make(chan struct{}),
	}
	process.cmd.Dir = t.TempDir()
	process.cmd.WaitDelay = time.Second
	process.cmd.Env = serverTestEnv(append([]string{
		"FBS_HTTP_ADDR=" + addr,
		"FBS_SHUTDOWN_TIMEOUT=1s",
	}, extraEnv...)...)
	process.cmd.Stdout = &process.stdout
	process.cmd.Stderr = &process.stderr
	if err := process.cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	go func() {
		process.waitErr = process.cmd.Wait()
		close(process.done)
	}()
	t.Cleanup(func() { process.stop(t) })
	return process
}

func (p *testServerProcess) waitReady() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return fmt.Errorf("server exited before readiness: %v", p.waitErr)
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		resp, err := p.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				select {
				case <-p.done:
					return fmt.Errorf("server exited at readiness: %v", p.waitErr)
				default:
					return nil
				}
			}
		}
		select {
		case <-p.done:
			return fmt.Errorf("server exited before readiness: %v", p.waitErr)
		case <-ctx.Done():
			return fmt.Errorf("server readiness: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *testServerProcess) stop(t *testing.T) {
	t.Helper()
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			t.Errorf("server exited before shutdown: %v\nstdout=%s\nstderr=%s", p.waitErr, p.stdout.String(), p.stderr.String())
			return
		default:
		}
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("signal server: %v", err)
		}
		select {
		case <-p.done:
			if p.waitErr != nil {
				t.Errorf("server shutdown: %v\nstdout=%s\nstderr=%s", p.waitErr, p.stdout.String(), p.stderr.String())
			}
		case <-time.After(5 * time.Second):
			if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("kill server: %v", err)
			}
			<-p.done
			t.Errorf("server did not exit after SIGTERM\nstdout=%s\nstderr=%s", p.stdout.String(), p.stderr.String())
		}
		p.client.CloseIdleConnections()
	})
}

func startTestServer(t *testing.T, extraEnv ...string) *testServerProcess {
	t.Helper()
	process := launchTestServer(t, buildServerBinary(t, "testendpoints"), extraEnv...)
	if err := process.waitReady(); err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, process.stdout.String(), process.stderr.String())
	}
	return process
}

func seedServerCredentials(t *testing.T, role iam.Role) (string, auth.IssuedToken, auth.SigV4Credentials, *metadata.User) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	issued, sigv4, user, err := auth.CreateBearerToken(context.Background(), metadata.NewUserRepository(db), "Server Test", role)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath, issued, sigv4, user
}
