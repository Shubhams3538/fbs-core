package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

type partialTimeoutConn struct {
	net.Conn
	body        bytes.Buffer
	writes      int
	readFroms   int
	deadlineErr error
	terminalErr error
}

func (c *partialTimeoutConn) SetWriteDeadline(time.Time) error { return c.deadlineErr }

func (c *partialTimeoutConn) Write(b []byte) (int, error) {
	c.writes++
	if c.writes == 1 {
		n := len(b) / 2
		c.body.Write(b[:n])
		return n, &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	if c.terminalErr != nil {
		return 0, c.terminalErr
	}
	return c.body.Write(b)
}

func (c *partialTimeoutConn) ReadFrom(r io.Reader) (int64, error) {
	c.readFroms++
	if c.readFroms == 1 {
		b := make([]byte, 127)
		n, err := r.Read(b)
		if err != nil {
			return 0, err
		}
		// Simulate buffered TCP fallback reading more than it writes.
		written := n / 2
		c.body.Write(b[:written])
		return int64(written), &net.OpError{Op: "readfrom", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	return io.Copy(&c.body, r)
}

func TestWriteTimeoutFileRetryPreservesBytes(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "limited-file"}[limited], func(t *testing.T) {
			body := bytes.Repeat([]byte("0123456789abcdef"), 65536)
			file, err := os.CreateTemp(t.TempDir(), "source")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write(body); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Seek(123, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			var src io.Reader = file
			want := body[123:]
			if limited {
				src = io.LimitReader(file, 200003)
				want = want[:200003]
			}
			underlying := &partialTimeoutConn{}
			conn := &writeTimeoutConn{Conn: underlying}
			conn.idleTimeout.Store(int64(time.Second))
			n, err := conn.ReadFrom(src)
			if err != nil || n != int64(len(want)) || !bytes.Equal(want, underlying.body.Bytes()) {
				t.Fatalf("n=%d err=%v actual=%d want=%d bytesEqual=%v", n, err, underlying.body.Len(), len(want), bytes.Equal(want, underlying.body.Bytes()))
			}
			if limited && src.(*io.LimitedReader).N != 0 {
				t.Fatalf("limited remaining=%d", src.(*io.LimitedReader).N)
			}
		})
	}
}

func TestWriteTimeoutGenericRetryPreservesBytes(t *testing.T) {
	body := bytes.Repeat([]byte("unique payload"), 10000)
	underlying := &partialTimeoutConn{}
	conn := &writeTimeoutConn{Conn: underlying}
	conn.idleTimeout.Store(int64(time.Second))
	n, err := conn.ReadFrom(struct{ io.Reader }{bytes.NewReader(body)})
	if err != nil || n != int64(len(body)) || !bytes.Equal(body, underlying.body.Bytes()) {
		t.Fatalf("n=%d err=%v got=%d", n, err, underlying.body.Len())
	}
	if underlying.readFroms != 0 {
		t.Fatalf("generic reader delegated to potentially consuming ReaderFrom")
	}
}

func TestWriteTimeoutErrorsAndReset(t *testing.T) {
	t.Run("no-progress-timeout", func(t *testing.T) {
		underlying := &partialTimeoutConn{terminalErr: os.ErrDeadlineExceeded}
		conn := &writeTimeoutConn{Conn: underlying}
		conn.idleTimeout.Store(int64(time.Second))
		n, err := conn.Write([]byte("abcdefgh"))
		if n != 4 || !errors.Is(err, os.ErrDeadlineExceeded) || underlying.writes != 2 {
			t.Fatalf("n=%d err=%v writes=%d", n, err, underlying.writes)
		}
	})
	t.Run("non-timeout-error", func(t *testing.T) {
		terminal := errors.New("broken pipe")
		underlying := &partialTimeoutConn{terminalErr: terminal}
		conn := &writeTimeoutConn{Conn: underlying}
		conn.idleTimeout.Store(int64(time.Second))
		n, err := conn.Write([]byte("abcdefgh"))
		if n != 4 || !errors.Is(err, terminal) || underlying.body.String() != "abcd" {
			t.Fatalf("n=%d err=%v body=%q", n, err, underlying.body.String())
		}
	})
	t.Run("renewal-failure", func(t *testing.T) {
		terminal := errors.New("closed connection")
		underlying := &partialTimeoutConn{deadlineErr: terminal}
		conn := &writeTimeoutConn{Conn: underlying}
		conn.idleTimeout.Store(int64(time.Second))
		n, err := conn.Write([]byte("abcdefgh"))
		if n != 0 || !errors.Is(err, terminal) || underlying.writes != 0 {
			t.Fatalf("n=%d err=%v writes=%d", n, err, underlying.writes)
		}
	})
	t.Run("deadline-reset", func(t *testing.T) {
		underlying := &partialTimeoutConn{}
		conn := &writeTimeoutConn{Conn: underlying}
		conn.idleTimeout.Store(int64(time.Second))
		if err := conn.SetWriteDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		if conn.idleTimeout.Load() != 0 {
			t.Fatal("object policy persisted")
		}
		n, err := conn.Write([]byte("abcdefgh"))
		if n != 4 || !errors.Is(err, os.ErrDeadlineExceeded) || underlying.writes != 1 {
			t.Fatalf("ordinary deadline was renewed: n=%d err=%v writes=%d", n, err, underlying.writes)
		}
	})
}
