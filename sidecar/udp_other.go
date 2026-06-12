//go:build !windows

package main

import "net"

// udpSender on non-Windows uses the standard net package. (This path only runs
// for the Linux dev-build stub; the real sidecar runs on Windows under Wine.)
type udpSender struct {
	conn net.Conn
}

func newUDPSender(addr string) (*udpSender, error) {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	return &udpSender{conn: conn}, nil
}

func (s *udpSender) send(b []byte) error {
	_, err := s.conn.Write(b)
	return err
}

func (s *udpSender) close() {
	s.conn.Close()
}
