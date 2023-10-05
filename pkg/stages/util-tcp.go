package stages

import (
	"context"
	"fmt"
	"io"
	"net"
	"syscall"
	"unsafe"

	"github.com/bgpfix/bgpipe/pkg/bgpipe"
	"golang.org/x/sys/unix"
)

func tcp_md5(md5pass string) func(net, addr string, c syscall.RawConn) error {
	if len(md5pass) == 0 {
		return nil
	}

	return func(net, addr string, c syscall.RawConn) error {
		// setup tcp sig
		var key [80]byte
		l := copy(key[:], md5pass)
		sig := unix.TCPMD5Sig{
			Flags:     unix.TCP_MD5SIG_FLAG_PREFIX,
			Prefixlen: 0,
			Keylen:    uint16(l),
			Key:       key,
		}

		// addr family
		switch net {
		case "tcp6", "udp6", "ip6":
			sig.Addr.Family = unix.AF_INET6
		default:
			sig.Addr.Family = unix.AF_INET
		}

		// setsockopt
		var err error
		c.Control(func(fd uintptr) {
			b := *(*[unsafe.Sizeof(sig)]byte)(unsafe.Pointer(&sig))
			err = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_MD5SIG_EXT, string(b[:]))
		})
		return err
	}

}

func tcp_handle(s *bgpipe.StageBase, conn net.Conn) error {
	s.Info().Msgf("connected %s -> %s", conn.LocalAddr(), conn.RemoteAddr())
	s.Event("connected", nil, conn.LocalAddr(), conn.RemoteAddr())
	defer conn.Close()

	// get tcp conn
	tcp, _ := conn.(*net.TCPConn)
	if tcp == nil {
		return fmt.Errorf("could not get TCPConn")
	}

	// discard data after conn.Close()
	if err := tcp.SetLinger(0); err != nil {
		s.Info().Err(err).Msg("SetLinger failed")
	}

	// variables for reader / writer
	type retval struct {
		n   int64
		err error
	}
	rch := make(chan retval, 1)
	wch := make(chan retval, 1)

	// read from conn -> write to s.Input
	go func() {
		n, err := io.Copy(s.Upstream(), conn)
		s.Trace().Err(err).Msg("connection reader returned")
		tcp.CloseRead()
		rch <- retval{n, err}
	}()

	// write to conn <- read from s.Output
	go func() {
		n, err := tcp.ReadFrom(s.Downstream())
		s.Trace().Err(err).Msg("connection writer returned")
		tcp.CloseWrite()
		wch <- retval{n, err}
	}()

	// wait for error on any side, or both sides EOF
	var read, wrote int64
	running := 2
	for running > 0 {
		select {
		case <-s.Ctx.Done():
			return context.Cause(s.Ctx)
		case r := <-rch:
			read = r.n
			running--
			if r.err != nil && r.err != io.EOF {
				return r.err
			}
		case w := <-wch:
			wrote = w.n
			running--
			if w.err != nil && w.err != io.EOF {
				return w.err
			}
		}
	}

	s.Info().Int64("read", read).Int64("wrote", wrote).Msg("connection closed")
	return nil
}