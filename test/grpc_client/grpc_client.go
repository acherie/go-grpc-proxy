package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	pb "github.com/acherie/go-grpc-proxy/grpc"
	"google.golang.org/grpc"
)

var (
	serverAddr = flag.String("server_addr", "localhost:10000", "The server address in the format of host:port")

	wg sync.WaitGroup
)

func runClient(id int) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())
	conn, err := grpc.Dial(*serverAddr, opts...)
	if err != nil {
		log.Fatalf("fail to dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewTransferClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := client.SendData(ctx)
	if err != nil {
		log.Fatalf("id %d, %v.SendData(_) = _, %v", id, client, err)
	}

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
				log.Printf("Failed to receive a cmd : %v", err)
			}
			log.Printf("Id %d, Got Cmd %v with Data %s", id, in.Cmd, in.Data)
		}
	}()

	cmds := []*pb.ProxyCmd{
		{Cmd: pb.ProxyCmd_Connect, Data: []byte("connect")},
		{Cmd: pb.ProxyCmd_Exchange, Data: []byte("senddata")},
	}
	if err := stream.Send(cmds[0]); err != nil {
		log.Fatalf("Failed to send a cmd: %v", err)
	}
	for i := 0; i < 10; i++ {
		cmd := &pb.ProxyCmd{
			Cmd:  pb.ProxyCmd_Exchange,
			Data: []byte(fmt.Sprintf("Client%d,Data%d", id, i)),
		}
		if err := stream.Send(cmd); err != nil {
			log.Fatalf("Failed to send a cmd: %v", err)
		}
		time.Sleep(1 * time.Second)
	}
	stream.CloseSend()
	<-waitc

	wg.Done()
}

func main() {
	flag.Parse()

	count := 2
	wg.Add(count)
	for i := 0; i < count; i++ {
		go runClient(i)
	}
	wg.Wait()
	fmt.Println("Shutdown")
}
