package main

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/pengubco/guiid/api/v1"
	"github.com/pengubco/guiid/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const maxIDsToStreamOnce = 1024

// Server is a wrapper of worker so that it can respond to gRPC.
type Server struct {
	pb.UnimplementedSnowflakeIDServer

	w *worker.Worker
}

// NextID returns an ID.
func (s *Server) NextID(_ context.Context, _ *emptypb.Empty) (*pb.IDResponse, error) {
	id, err := s.w.NextID()

	if err != nil {
		switch err {
		case worker.ErrNotReady:
			return nil, status.Error(codes.Unavailable, "not ready. try later")
		case worker.ErrClockDriftTryLater:
			return nil, status.Error(codes.Unavailable, "clock drift. try later")
		case worker.ErrClockDriftTooLargeToRecover:
			return nil, status.Error(codes.Unavailable, "clock drift. try later")
			return nil, status.Error(codes.Unavailable, "try later")
		}
	}
	return &pb.IDResponse{Id: id}, nil
}

// must embed in grpc-go. See discussion at https://github.com/grpc/grpc-go/issues/3794
func (s *Server) mustEmbedUnimplementedSnowflakeIDServer() {
}

// NextMultipleIDs streams n IDs to client.
func (s *Server) NextMultipleIDs(n *wrapperspb.UInt32Value, stream pb.SnowflakeID_NextMultipleIDsServer) error {
	cnt := n.Value
	if cnt > maxIDsToStreamOnce {
		return fmt.Errorf("too many IDs to return in one stream. %d > %d", cnt, maxIDsToStreamOnce)
	}
	var id int64
	var err error
	for ; cnt > 0; cnt-- {
		if id, err = s.w.NextID(); err != nil {
			return status.Error(codes.Unavailable, err.Error())
		}
		if err = stream.Send(&pb.IDResponse{Id: id}); err != nil {
			return fmt.Errorf("error sending message to stream, %v", err)
		}
	}
	return nil
}
