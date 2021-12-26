package main

import (
	"context"
	"flag"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/acherie/go-grpc-proxy/socks"
	"google.golang.org/grpc"

	pb "github.com/acherie/go-grpc-proxy/grpc"
)

const payloadSizeMask = 0x3FFF // 16*1024 - 1

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

			runClient(server, tgt.String(), c)
		}()
	}
}

func runClient(serverAddr, targetAddr string, c net.Conn) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())
	conn, err := grpc.Dial(serverAddr, opts...)
	if err != nil {
		logf("fail to dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewTransferClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := client.SendData(ctx)
	if err != nil {
		logf("%v.SendData(_) = _, %v", client, err)
	}

	idCh := make(chan int32)
	waitc := make(chan struct{})
	go func() {
		for {
			in, err := stream.Recv()
			if err == io.EOF {
				//read done.
				close(waitc)
				return
			}
			if err != nil {
				logf("Failed to receive a cmd : %v", err)
			}

			if in.Cmd == pb.ProxyCmd_Connect {
				logf("receive clientId from server: %d", in.ClientId)
				idCh <- in.ClientId
				close(idCh)
				continue
			}

			clientId := in.ClientId
			logf("cid %d, receive data from server", clientId)
			n, err := c.Write(in.Data)
			if err != nil {
				logf("failed to write to source %v", err)
			}
			logf("cid %d, write %d bytes to source", clientId, n)
		}
	}()

	connectCmd := &pb.ProxyCmd{
		Cmd:  pb.ProxyCmd_Connect,
		Data: []byte(targetAddr),
	}
	if err := stream.Send(connectCmd); err != nil {
		logf("Failed to send a connect cmd: %v", err)
	}

	clientId := <-idCh
	logf("recv clientId %d", clientId)
	buf := make([]byte, payloadSizeMask)
	for {
		n, err := c.Read(buf)
		if err != nil {
			logf("failed to readAll from source %s, %v", c.LocalAddr().String(), err)
			break
		}
		if n <= 0 {
			continue
		}
		logf("cid %d, send data to server %d", clientId, n)
		exchangeCmd := &pb.ProxyCmd{
			Cmd:      pb.ProxyCmd_Exchange,
			ClientId: clientId,
			Data:     buf[:n],
		}
		if err := stream.Send(exchangeCmd); err != nil {
			logf("failed send data to server: %v", err)
			break
		}
	}
	stream.CloseSend()
	<-waitc
}

type proxyServer struct {
	pb.UnimplementedTransferServer
}

var cIdCounter int32 = 0
var cConnMap sync.Map

func (s *proxyServer) SendData(stream pb.Transfer_SendDataServer) error {
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			logf("stream recv EOF")
			return nil
		}
		if err != nil {
			logf("failed recv %v", err)
			return err
		}

		if in.Cmd == pb.ProxyCmd_Connect {
			targetAddr := string(in.Data)
			cId := atomic.AddInt32(&cIdCounter, 1)
			rc, err := net.Dial("tcp", targetAddr)
			if err != nil {
				logf("failed to connect to target %s: %v", targetAddr, err)
				return err
			}

			logf("proxy %d <-> %s", cId, targetAddr)

			cConnMap.Store(cId, rc)
			respCmd := &pb.ProxyCmd{
				Cmd:      pb.ProxyCmd_Connect,
				ClientId: cId,
			}
			if err := stream.Send(respCmd); err != nil {
				logf("failed send to client %v", err)
				return err
			}

			go func() {
				buf := make([]byte, payloadSizeMask)
				for {
					n, err := rc.Read(buf)
					if err != nil {
						logf("failed to read from target %s, %v", rc.RemoteAddr().String(), err)
						break
					}
					if n <= 0 {
						continue
					}
					logf("cid %d, server send data to client %d", cId, n)
					exchangeCmd := &pb.ProxyCmd{
						Cmd:      pb.ProxyCmd_Exchange,
						ClientId: cId,
						Data:     buf[:n],
					}
					if err := stream.Send(exchangeCmd); err != nil {
						logf("failed to send cmd to client: %v", err)
						break
					}
				}
			}()

		} else {
			data := in.Data
			clientId := in.ClientId
			rc, ok := cConnMap.Load(clientId)
			if !ok {
				logf("failed to load client %d conn", clientId)
				continue
			}
			logf("cid %d, server write data to target %d", clientId, len(data))
			tc := rc.(net.Conn)
			n, err := tc.Write(data)
			if err != nil {
				logf("failed write data to target %s, %v", tc.RemoteAddr().String(), err)
				continue
			}
			logf("cid %d, server write data to target %d success", clientId, n)
		}
	}
}

func newServer() *proxyServer {
	s := &proxyServer{}
	return s
}

func tcpRemote(addr string) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		logf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterTransferServer(grpcServer, newServer())
	logf("grpc server listening at %v", addr)
	if err := grpcServer.Serve(lis); err != nil {
		logf("failed to serve: %v", err)
	}
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

	if flags.Client != "" { // client mode
		go socksLocal(flags.Socks, flags.Client)
	}

	if flags.Server != "" { // server mode
		go tcpRemote(flags.Server)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
