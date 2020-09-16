package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/core/transaction"
	pb "github.com/MinterTeam/node-grpc-gateway/api_pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

// Transaction returns transaction info.
func (s *Service) Transaction(ctx context.Context, req *pb.TransactionRequest) (*pb.TransactionResponse, error) {
	if len(req.Hash) < 3 {
		return new(pb.TransactionResponse), status.Error(codes.InvalidArgument, "invalid hash")
	}
	decodeString, err := hex.DecodeString(req.Hash[2:])
	if err != nil {
		return new(pb.TransactionResponse), status.Error(codes.InvalidArgument, err.Error())
	}

	tx, err := s.client.Tx(decodeString, false)
	if err != nil {
		return new(pb.TransactionResponse), status.Error(codes.FailedPrecondition, err.Error())
	}

	decodedTx, _ := transaction.TxDecoder.DecodeFromBytes(tx.Tx)
	sender, _ := decodedTx.Sender()

	tags := make(map[string]string)
	for _, tag := range tx.TxResult.Events[0].Attributes {
		tags[string(tag.Key)] = string(tag.Value)
	}

	cState, err := s.blockchain.GetStateForHeight(uint64(tx.Height))
	if err != nil {
		return new(pb.TransactionResponse), status.Error(codes.NotFound, err.Error())
	}

	cState.RLock()
	defer cState.RUnlock()

	if timeoutStatus := s.checkTimeout(ctx); timeoutStatus != nil {
		return new(pb.TransactionResponse), timeoutStatus.Err()
	}
	dataStruct, err := encode(decodedTx.GetDecodedData(), cState.Coins())
	if err != nil {
		return new(pb.TransactionResponse), status.Error(codes.Internal, err.Error())
	}

	return &pb.TransactionResponse{
		Hash:     "Mt" + strings.ToLower(tx.Tx.String()),
		RawTx:    fmt.Sprintf("%x", []byte(tx.Tx)),
		Height:   fmt.Sprintf("%d", tx.Height),
		Index:    fmt.Sprintf("%d", tx.Index),
		From:     sender.String(),
		Nonce:    fmt.Sprintf("%d", decodedTx.Nonce),
		GasPrice: fmt.Sprintf("%d", decodedTx.GasPrice),
		GasCoin:  decodedTx.GasCoin.String(),
		Gas:      fmt.Sprintf("%d", decodedTx.Gas()),
		Type:     fmt.Sprintf("%d", uint8(decodedTx.Type)),
		Data:     dataStruct,
		Payload:  decodedTx.Payload,
		Tags:     tags,
		Code:     fmt.Sprintf("%d", tx.TxResult.Code),
		Log:      tx.TxResult.Log,
	}, nil
}
