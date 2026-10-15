package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

type writeTimeoutConnKey struct{}

// WriteTimeoutConnContext attaches the connection's transfer policy to each
// request. It must be paired with NewWriteTimeoutListener.
func WriteTimeoutConnContext(ctx context.Context, conn net.Conn) context.Context {
	if conn, ok := conn.(*writeTimeoutConn); ok {
		return context.WithValue(ctx, writeTimeoutConnKey{}, conn)
	}
	return ctx
}

// EnableWriteIdleTimeout replaces the total response deadline for an object
// transfer. Ordinary responses continue to use http.Server.WriteTimeout.
func EnableWriteIdleTimeout(r *http.Request, timeout time.Duration) error {
	conn, ok := r.Context().Value(writeTimeoutConnKey{}).(*writeTimeoutConn)
	if !ok || timeout <= 0 || r.ProtoMajor != 1 {
		return nil
	}
	if err := conn.Conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	conn.idleTimeout.Store(int64(timeout))
	return nil
}

type writeTimeoutListener struct {
	net.Listener
}

// NewWriteTimeoutListener preserves the underlying connection's optimized
// file transfers while allowing object responses to use a progress timeout.
func NewWriteTimeoutListener(listener net.Listener) net.Listener {
	return &writeTimeoutListener{Listener: listener}
}

func (l *writeTimeoutListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &writeTimeoutConn{Conn: conn}, nil
}

type writeTimeoutConn struct {
	net.Conn
	idleTimeout atomic.Int64
}

func (c *writeTimeoutConn) NetConn() net.Conn {
	return c.Conn
}

func (c *writeTimeoutConn) SetWriteDeadline(deadline time.Time) error {
	// net/http clears the deadline after each response and sets the next
	// request's deadline. Never let a download's policy leak into keep-alive.
	c.idleTimeout.Store(0)
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *writeTimeoutConn) Write(body []byte) (int, error) {
	timeout := time.Duration(c.idleTimeout.Load())
	if timeout <= 0 {
		return c.Conn.Write(body)
	}
	var total int
	for len(body) > 0 {
		if err := c.Conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return total, err
		}
		n, err := c.Conn.Write(body)
		total += n
		body = body[n:]
		if err != nil && !madeWriteProgress(int64(n), err) {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func (c *writeTimeoutConn) ReadFrom(src io.Reader) (int64, error) {
	readerFrom, ok := c.Conn.(io.ReaderFrom)
	fileSource := src
	limited, isLimited := src.(*io.LimitedReader)
	if isLimited {
		fileSource = limited.R
	}
	file, isFile := fileSource.(*os.File)
	if !ok || !isFile {
		// A generic ReaderFrom can consume bytes it fails to write. Use our
		// retrying Write instead, so those bytes remain available on timeout.
		return io.Copy(struct{ io.Writer }{c}, src)
	}
	timeout := time.Duration(c.idleTimeout.Load())
	if timeout <= 0 {
		return readerFrom.ReadFrom(src)
	}
	var total int64
	for {
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return total, err
		}
		var remaining int64
		if isLimited {
			remaining = limited.N
		}
		if err := c.Conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return total, err
		}
		n, err := readerFrom.ReadFrom(src)
		total += n
		if !madeWriteProgress(n, err) {
			return total, err
		}
		// A filesystem without sendfile support can fall back to buffered
		// copying, which reads ahead. Resume at bytes actually sent, not read.
		if _, err := file.Seek(offset+n, io.SeekStart); err != nil {
			return total, err
		}
		if isLimited {
			limited.N = remaining - n
		}
	}
}

func madeWriteProgress(n int64, err error) bool {
	var netError net.Error
	return n > 0 && errors.As(err, &netError) && netError.Timeout()
}
