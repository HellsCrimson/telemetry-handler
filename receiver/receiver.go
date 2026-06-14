package receiver

import (
	"context"
	"errors"
	"log"
	"net"
	"time"
)

type Handler func(context.Context, []byte) error

func Listen(ctx context.Context, addr string, handle Handler) error {
	packetConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer packetConn.Close()

	// Large enough for one lmu-bridge wire chunk (~60KB) as well as Forza's
	// 324-byte packets; the LMU sidecar splits big frames into chunks that each
	// fit a single datagram, and the app reassembles them.
	buf := make([]byte, 65536)
	var lastErrLog time.Time
	for {
		if err := packetConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return err
		}

		n, _, err := packetConn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			return err
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])
		// A handler error is per-packet and non-fatal: a malformed packet or a
		// downstream side effect (e.g. MOZA serial hiccup, recording write)
		// must not tear down the listener. Log it (throttled to avoid spam at
		// packet rate) and keep receiving. Only the socket errors above end the
		// loop.
		if err := handle(ctx, packet); err != nil {
			if time.Since(lastErrLog) > time.Second {
				log.Printf("receiver: handler error (continuing): %v", err)
				lastErrLog = time.Now()
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}
