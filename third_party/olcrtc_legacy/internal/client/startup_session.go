package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/openlibrecommunity/olcrtc/internal/muxconn"
	"github.com/openlibrecommunity/olcrtc/internal/runtime"
	"github.com/xtaci/smux"
)

// openInitialSession retries a stream closed by the server's carrier rebuild.
// The local bridge can open just before the server replaces its old session.
func (c *Client) openInitialSession(ctx context.Context) (*smux.Session, *smux.Stream, string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if attempt != 0 {
			c.resetLinkPeer()
		}
		if err := waitForPeer(ctx, c.ln); err != nil {
			return nil, nil, "", err
		}
		if err := waitForSendable(ctx, c.ln); err != nil {
			return nil, nil, "", err
		}
		conn := muxconn.New(c.ln, c.cipher)
		c.sessMu.Lock()
		c.conn = conn
		c.sessMu.Unlock()
		sess, err := smux.Client(conn, runtime.SmuxConfigFor(c.ln))
		if err != nil {
			_ = conn.Close()
			return nil, nil, "", fmt.Errorf("smux client: %w", err)
		}
		control, sid, err := openControlStream(ctx, sess, c.deviceID, c.claims)
		if err == nil {
			return sess, control, sid, nil
		}
		_ = sess.Close()
		_ = conn.Close()
		if attempt == 1 || ctx.Err() != nil || !startupStreamClosed(err) {
			return nil, nil, "", fmt.Errorf("handshake: %w", err)
		}
	}
	panic("unreachable")
}

func startupStreamClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)
}
