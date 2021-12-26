package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"

	"google.golang.org/grpc"

	pb "github.com/acherie/go-grpc-proxy/grpc"
)

var (
	port = flag.Int("port", 10000, "The server port")
)

type proxyServer struct {
	pb.UnimplementedTransferServer
}

func (s *proxyServer) SendData(stream pb.Transfer_SendDataServer) error {
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		fmt.Printf("Recev Cmd: %v, Data: %s\n", in.Cmd, in.Data)

		respCmd := &pb.ProxyCmd{
			Cmd:  pb.ProxyCmd_Exchange,
			Data: []byte("Response"),
		}
		if err := stream.Send(respCmd); err != nil {
			return err
		}
	}
}

func newServer() *proxyServer {
	s := &proxyServer{}
	return s
}

func main() {
	flag.Parse()
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterTransferServer(grpcServer, newServer())
	log.Printf("server listening at %v", lis.Addr())
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
