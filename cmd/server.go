package main

import (
	"context"

	pb "github.com/pengubco/snowflake-id/api/v1"
	"github.com/pengubco/snowflake-id/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server is a wrapper of worker so that it can respond to gRPC.
type Server struct {
	pb.UnimplementedSnowflakeIDServer

	w *worker.Worker
}

// NextID returns a snowflake ID.
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
