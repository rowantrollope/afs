package controlplane

import (
	"io"
	"net"
	"net/http"
	"time"
)

const cliHTTPIdleTimeout = 2 * time.Minute

type idleDeadlineConn struct {
	net.Conn
	idle time.Duration
}

// Refresh both directions: net/http waits for response headers concurrently
// with uploading, so progress writing must keep that blocked read alive too.
func (c *idleDeadlineConn) Read(p []byte) (int, error) {
	if err := c.SetDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		_ = c.SetDeadline(time.Now().Add(c.idle))
	}
	return n, err
}

func (c *idleDeadlineConn) Write(p []byte) (int, error) {
	if err := c.SetDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)
	if n > 0 {
		_ = c.SetDeadline(time.Now().Add(c.idle))
	}
	return n, err
}

type idleRequestBody struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

func (b *idleRequestBody) Read(p []byte) (int, error) {
	if err := b.controller.SetReadDeadline(time.Now().Add(b.idle)); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}
