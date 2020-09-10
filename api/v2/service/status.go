package service

import (
	"context"
	"fmt"
	pb "github.com/MinterTeam/node-grpc-gateway/api_pb"
	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

// Status returns current min gas price.
func (s *Service) Status(context.Context, *empty.Empty) (*pb.StatusResponse, error) {
	result, err := s.client.Status()
	if err != nil {
		return new(pb.StatusResponse), status.Error(codes.Internal, err.Error())
	}

	cState := s.blockchain.CurrentState()
	cState.RLock()
	defer cState.RUnlock()

	return &pb.StatusResponse{
		Version:           s.version,
		LatestBlockHash:   fmt.Sprintf("%X", result.SyncInfo.LatestBlockHash),
		LatestAppHash:     fmt.Sprintf("%X", result.SyncInfo.LatestAppHash),
		LatestBlockHeight: fmt.Sprintf("%d", result.SyncInfo.LatestBlockHeight),
		LatestBlockTime:   result.SyncInfo.LatestBlockTime.Format(time.RFC3339Nano),
		KeepLastStates:    fmt.Sprintf("%d", s.minterCfg.BaseConfig.KeepLastStates),
		TotalSlashed:      cState.App().GetTotalSlashed().String(),
		CatchingUp:        result.SyncInfo.CatchingUp,
		PublicKey:         fmt.Sprintf("Mp%x", result.ValidatorInfo.PubKey.Bytes()[5:]),
		NodeId:            string(result.NodeInfo.ID()),
	}, nil
}
