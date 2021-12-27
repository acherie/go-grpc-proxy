package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/acherie/go-grpc-proxy/socks"
	"github.com/gorilla/websocket"
)

var config struct {
	Verbose bool
}

func socksLocal(addr, server string) {
	logf("SOCKS proxy %s <-> %s", addr, server)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		logf("failed to listen on %s: %v", addr, err)
		return
	}

	for {
		c, err := l.Accept()
		if err != nil {
			logf("failed to accept: %s", err)
			continue
		}

		go func() {
			defer c.Close()

			tgt, err := socks.Handshake(c)
			if err != nil {
				logf("failed to get target address: %v", err)
				return
			}

			u := url.URL{Scheme: "ws", Host: server, Path: "/"}
			logf("connect to server %s", u.String())

			rc, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
			if err != nil {
				logf("failed to connect to server %v: %v", server, err)
				return
			}
			defer rc.Close()

			if rc.WriteMessage(websocket.TextMessage, []byte(tgt.String())) != nil {
				logf("failed to send target address: %v", err)
				return
			}

			logf("proxy %s <-> %s <-> %s", c.RemoteAddr(), server, tgt)
			if err = relay(c, rc); err != nil {
				logf("relay error: %v", err)
			}
			logf("relay down %s <-> %s <-> %s", c.RemoteAddr(), server, tgt)
		}()
	}
}

var upgrader = websocket.Upgrader{} // use default options

func handleServerWs(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logf("failed ws upgrade: %v", err)
		return
	}
	defer c.Close()

	_, tgt, err := c.ReadMessage()
	if err != nil {
		logf("failed to get target addr from %s: %v", c.RemoteAddr(), err)
		return
	}

	rc, err := net.Dial("tcp", string(tgt))
	if err != nil {
		logf("failed to connect to target %s", tgt)
	}
	defer rc.Close()

	logf("proxy %s <-> %s", c.RemoteAddr(), tgt)
	if err = relay(rc, c); err != nil {
		logf("relay error: %v", err)
	}
	logf("relay down %s <-> %s", c.RemoteAddr(), tgt)
}

func relay(c net.Conn, rc *websocket.Conn) error {
	var err, err1 error
	var wg sync.WaitGroup
	var wait = 5 * time.Second
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			var msg []byte
			rc.SetReadDeadline(time.Now().Add(wait))
			_, msg, err1 = rc.ReadMessage()
			if err1 != nil {
				// logf("failed to read from ws: %v", err1)
				return
			}
			var n int
			n, err1 = c.Write(msg)
			if err1 != nil {
				// logf("failed to write to c: %v", err1)
				return
			}
			logf("write from ws to c: %d", n)
		}
	}()

	buf := make([]byte, 32*1024)
	var n int
	for {
		c.SetReadDeadline(time.Now().Add(wait))
		n, err = c.Read(buf)
		if err != nil {
			// logf("failed to read from c: %v", err)
			break
		}
		err = rc.WriteMessage(websocket.BinaryMessage, buf[:n])
		if err != nil {
			// logf("failed to write message to ws: %v", err)
			break
		}
		logf("write from c to ws: %d", n)
	}
	wg.Wait()
	if err1 != nil && !errors.Is(err1, os.ErrDeadlineExceeded) {
		return err1
	}
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		return err
	}
	return nil
}

func tcpRemote(addr string) {
	http.HandleFunc("/", handleServerWs)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func main() {
	var flags struct {
		Client string
		Server string
		Socks  string
	}

	flag.BoolVar(&config.Verbose, "verbose", false, "Verbose mode")
	flag.StringVar(&flags.Client, "c", "", "client connect address or url")
	flag.StringVar(&flags.Server, "s", "", "server listen address or url")
	flag.StringVar(&flags.Socks, "socks", ":1080", "(client only) SOCKS listen address")
	flag.Parse()

	if flags.Client == "" && flags.Server == "" {
		flag.Usage()
		return
	}

	if flags.Client != "" {
		go socksLocal(flags.Socks, flags.Client)
	}

	if flags.Server != "" {
		go tcpRemote(flags.Server)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
