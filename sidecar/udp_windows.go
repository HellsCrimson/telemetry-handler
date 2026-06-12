//go:build windows

package main

import (
	"fmt"
	"net"
	"strconv"
	"syscall"
	"unsafe"
)

// udpSender sends datagrams via raw winsock, deliberately bypassing Go's net
// package. net's UDP socket setup issues a SIO_UDP_NETRESET WSAIoctl
// (go.dev/issue/68614) that Wine doesn't implement, so net.Dial("udp") fails
// under Wine/Proton with WSAEOPNOTSUPP (#10045).
//
// Note: Go's plain syscall.Sendto is a stub on Windows (returns EWINDOWS,
// "not supported by windows"), so we send with WSASendTo instead.
type udpSender struct {
	fd     syscall.Handle
	rsa    syscall.RawSockaddrAny // destination as a raw sockaddr_in
	rsaLen int32
}

func newUDPSender(addr string) (*udpSender, error) {
	host, portStr, err := net.SplitHostPort(addr) // pure parsing, no socket
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("invalid host %q", host)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("only IPv4 destinations are supported: %q", host)
	}

	var wsa syscall.WSAData
	if err := syscall.WSAStartup(uint32(0x202), &wsa); err != nil {
		return nil, fmt.Errorf("WSAStartup: %w", err)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		syscall.WSACleanup()
		return nil, fmt.Errorf("socket: %w", err)
	}

	s := &udpSender{fd: fd, rsaLen: int32(unsafe.Sizeof(syscall.RawSockaddrInet4{}))}
	sa := syscall.RawSockaddrInet4{
		Family: syscall.AF_INET,
		Port:   htons(uint16(port)), // network byte order
	}
	copy(sa.Addr[:], ip4)
	*(*syscall.RawSockaddrInet4)(unsafe.Pointer(&s.rsa)) = sa
	return s, nil
}

func (s *udpSender) send(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	buf := syscall.WSABuf{Len: uint32(len(b)), Buf: &b[0]}
	var sent uint32
	return syscall.WSASendTo(s.fd, &buf, 1, &sent, 0, &s.rsa, s.rsaLen, nil, nil)
}

func (s *udpSender) close() {
	syscall.Closesocket(s.fd)
	syscall.WSACleanup()
}

// htons converts a port to network byte order (winsock sockaddr wants big-endian).
func htons(p uint16) uint16 { return p<<8 | p>>8 }
