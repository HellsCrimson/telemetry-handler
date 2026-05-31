package receiver

import (
	"context"
	"errors"
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

	buf := make([]byte, 2048)
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
		if err := handle(ctx, packet); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}
