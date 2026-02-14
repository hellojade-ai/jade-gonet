package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	pb "gonet/grpc/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type server struct {
	pb.UnimplementedGonetServiceServer
}

// =============================================================================
// 1. Unary RPC — Single request, single response
//    Client sends one message, server replies with one message.
//    Use case: fetching a single resource, authentication, lookups.
// =============================================================================

func (s *server) GetItem(ctx context.Context, req *pb.GetItemRequest) (*pb.GetItemResponse, error) {
	log.Printf("[unary] GetItem called with id=%s", req.Id)
	return &pb.GetItemResponse{
		Item: &pb.Item{
			Id:        req.Id,
			Name:      fmt.Sprintf("item-%s", req.Id),
			Data:      "fetched via unary RPC",
			Timestamp: time.Now().Unix(),
		},
	}, nil
}

// =============================================================================
// 2. Server Streaming — Single request, stream of responses
//    Client sends one request, server streams back multiple messages.
//    Use case: listing results, real-time feeds, log tailing.
// =============================================================================

func (s *server) ListItems(req *pb.ListItemsRequest, stream grpc.ServerStreamingServer[pb.Item]) error {
	log.Printf("[server-stream] ListItems called with prefix=%s count=%d", req.Prefix, req.Count)
	count := int(req.Count)
	if count <= 0 {
		count = 5
	}
	for i := 0; i < count; i++ {
		item := &pb.Item{
			Id:        fmt.Sprintf("%s-%d", req.Prefix, i),
			Name:      fmt.Sprintf("%s item %d", req.Prefix, i),
			Data:      fmt.Sprintf("streamed item %d of %d", i+1, count),
			Timestamp: time.Now().Unix(),
		}
		if err := stream.Send(item); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond) // simulate paced delivery
	}
	return nil
}

// =============================================================================
// 3. Client Streaming — Stream of requests, single response
//    Client sends multiple messages, server collects and replies once.
//    Use case: file upload, batch data ingestion, aggregation.
// =============================================================================

func (s *server) SendItems(stream grpc.ClientStreamingServer[pb.Item, pb.SendItemsResponse]) error {
	log.Println("[client-stream] SendItems started")
	var count int32
	var lastItem string
	for {
		item, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.SendItemsResponse{
				ReceivedCount: count,
				Summary:       fmt.Sprintf("received %d items, last was: %s", count, lastItem),
			})
		}
		if err != nil {
			return err
		}
		count++
		lastItem = item.Name
		log.Printf("[client-stream] received item %d: %s", count, item.Name)
	}
}

// =============================================================================
// 4. Bidirectional Streaming — Both sides stream simultaneously
//    Client and server both send streams of messages independently.
//    Use case: chat, real-time collaboration, game state sync.
// =============================================================================

func (s *server) Exchange(stream grpc.BidiStreamingServer[pb.ExchangeMessage, pb.ExchangeMessage]) error {
	log.Println("[bidi-stream] Exchange started")
	var seq atomic.Int64
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		log.Printf("[bidi-stream] received from %s: %s", msg.Sender, msg.Content)

		reply := &pb.ExchangeMessage{
			Sender:   "server",
			Content:  fmt.Sprintf("ack: %s", msg.Content),
			Sequence: seq.Add(1),
		}
		if err := stream.Send(reply); err != nil {
			return err
		}
	}
}

// =============================================================================
// Main
// =============================================================================

func main() {
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterGonetServiceServer(s, &server{})
	reflection.Register(s) // enables grpcurl discovery

	log.Println("gRPC server starting on :50051")
	log.Fatal(s.Serve(lis))
}
