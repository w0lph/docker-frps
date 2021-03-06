package main

import (
	"crypto/tls"
	vhost "github.com/inconshreveable/go-vhost"
    "golang.org/x/crypto/acme/autocert"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const (
	muxTimeout            = 10 * time.Second
	defaultConnectTimeout = 10000 // milliseconds
)

type ProxyServer struct {
	*log.Logger
    *autocert.Manager
	// these are for easier testing
	mux   *vhost.TLSMuxer
	ready chan int
}

func (s *ProxyServer) Run() error {
	// bind a port to handle TLS connections
	l, err := net.Listen("tcp", ":444")
	if err != nil {
		return err
	}
	s.Printf("Serving connections on %v", l.Addr())

	// start muxing on it
	s.mux, err = vhost.NewTLSMuxer(l, muxTimeout)
	if err != nil {
		return err
	}

	// custom error handler so we can log errors
	go func() {
		for {
			conn, err := s.mux.NextError()

			if conn == nil {
				s.Printf("Failed to mux next connection, error: %v", err)
				if _, ok := err.(vhost.Closed); ok {
					return
				} else {
					continue
				}
			}
		}
	}()

	// we're ready, signal it for testing
	if s.ready != nil {
		close(s.ready)
	}

	return nil
}

func (s *ProxyServer) addFrontend(name string, passthrough bool)  (err error) {
	fl, err := s.mux.Listen(name)
	if err != nil {
		return err
	}
    if passthrough {
    	go s.runFrontend(name, nil, fl)
    } else {
	    tlsconfig := &tls.Config{
		    GetCertificate: s.Manager.GetCertificate,
		    NextProtos: []string{"http/1.1"},
	    }

        go s.runFrontend(name, tlsconfig, fl)
    }

    return nil
}

func (s *ProxyServer) runFrontend(name string, tlsConfig *tls.Config, l net.Listener) {

	for {
		// accept next connection to this frontend
		conn, err := l.Accept()
		if err != nil {
			s.Printf("Failed to accept new connection for '%v': %v", conn.RemoteAddr())
			if e, ok := err.(net.Error); ok {
				if e.Temporary() {
					continue
				}
			}
			return
		}

		// proxy the connection to an backend
		go s.proxyConnection(conn, tlsConfig)
	}
}

func (s *ProxyServer) proxyConnection(c net.Conn, tlsConfig *tls.Config) (err error) {
    var backend string
	// unwrap if tls cert/key was specified
	if tlsConfig != nil {
        var tlsc = tls.Server(c, tlsConfig)
        tlsc.Handshake()
        c = net.Conn(tlsc)
        backend = "localhost:80"
    } else {
        backend = "localhost:443"
    }

	// dial the backend
	upConn, err := net.DialTimeout("tcp", backend, time.Duration(defaultConnectTimeout)*time.Millisecond)
	if err != nil {
		s.Printf("Failed to dial backend connection %v: %v", backend, err)
		c.Close()
		return
	}

	// join the connections
	s.joinConnections(c, upConn)
	return
}

func (s *ProxyServer) joinConnections(c1 net.Conn, c2 net.Conn) {
	var wg sync.WaitGroup
	halfJoin := func(dst net.Conn, src net.Conn) {
		defer wg.Done()
		defer dst.Close()
		defer src.Close()
		n, err := io.Copy(dst, src)
		if err != nil {
			s.Printf("Copy from %v to %v failed after %d bytes with error %v", src.RemoteAddr(), dst.RemoteAddr(), n, err)
		}
	}

	wg.Add(2)
	go halfJoin(c1, c2)
	go halfJoin(c2, c1)
	wg.Wait()
}

