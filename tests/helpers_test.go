package tests

import (
	"crypto/ecdsa"
	"github.com/MinterTeam/minter-go-node/cmd/utils"
	"github.com/MinterTeam/minter-go-node/config"
	"github.com/MinterTeam/minter-go-node/coreV2/minter"
	"github.com/MinterTeam/minter-go-node/coreV2/transaction"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	"github.com/MinterTeam/minter-go-node/crypto"
	"github.com/MinterTeam/minter-go-node/rlp"
	"github.com/tendermint/go-amino"
	tmTypes "github.com/tendermint/tendermint/abci/types"
	tmTypes1 "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/proto/tendermint/version"
	"time"
)

// CreateApp creates and returns new Blockchain instance
func CreateApp(state types.AppState) *minter.Blockchain {
	jsonState, err := amino.MarshalJSON(state)
	if err != nil {
		panic(err)
	}

	storage := utils.NewStorage("", "")
	cfg := config.GetConfig(storage.GetMinterHome())
	cfg.DBBackend = "memdb"
	app := minter.NewMinterBlockchain(storage, cfg, nil)
	app.InitChain(tmTypes.RequestInitChain{
		Time:    time.Now(),
		ChainId: "test",
		Validators: []tmTypes.ValidatorUpdate{
			tmTypes.Ed25519ValidatorUpdate([]byte{}, 1),
		},
		AppStateBytes: jsonState,
	})

	return app
}

// SendCommit sends Commit message to given Blockchain instance
func SendCommit(app *minter.Blockchain) tmTypes.ResponseCommit {
	return app.Commit()
}

// SendBeginBlock sends BeginBlock message to given Blockchain instance
func SendBeginBlock(app *minter.Blockchain) tmTypes.ResponseBeginBlock {
	return app.BeginBlock(tmTypes.RequestBeginBlock{
		Hash: nil,
		Header: tmTypes1.Header{
			Version:            version.Consensus{},
			ChainID:            "",
			Height:             1,
			Time:               time.Time{},
			LastBlockId:        tmTypes1.BlockID{},
			LastCommitHash:     nil,
			DataHash:           nil,
			ValidatorsHash:     nil,
			NextValidatorsHash: nil,
			ConsensusHash:      nil,
			AppHash:            nil,
			LastResultsHash:    nil,
			EvidenceHash:       nil,
			ProposerAddress:    nil,
		},
		LastCommitInfo: tmTypes.LastCommitInfo{
			Round: 0,
			Votes: nil,
		},
		ByzantineValidators: nil,
	})
}

// SendEndBlock sends EndBlock message to given Blockchain instance
func SendEndBlock(app *minter.Blockchain) tmTypes.ResponseEndBlock {
	return app.EndBlock(tmTypes.RequestEndBlock{
		Height: 0,
	})
}

// CreateTx composes and returns Tx with given params.
// Nonce, chain id, gas price, gas coin and signature type fields are auto-filled.
func CreateTx(app *minter.Blockchain, address types.Address, txType transaction.TxType, data interface{}) transaction.Transaction {
	nonce := app.CurrentState().Accounts().GetNonce(address) + 1
	bData, err := rlp.EncodeToBytes(data)
	if err != nil {
		panic(err)
	}

	tx := transaction.Transaction{
		Nonce:         nonce,
		ChainID:       types.CurrentChainID,
		GasPrice:      1,
		GasCoin:       types.GetBaseCoinID(),
		Type:          txType,
		Data:          bData,
		SignatureType: transaction.SigTypeSingle,
	}

	return tx
}

// SendTx sends DeliverTx message to given Blockchain instance
func SendTx(app *minter.Blockchain, bytes []byte) tmTypes.ResponseDeliverTx {
	return app.DeliverTx(tmTypes.RequestDeliverTx{
		Tx: bytes,
	})
}

// SignTx returns bytes of signed with given pk transaction
func SignTx(pk *ecdsa.PrivateKey, tx transaction.Transaction) []byte {
	err := tx.Sign(pk)
	if err != nil {
		panic(err)
	}

	b, _ := rlp.EncodeToBytes(tx)

	return b
}

// CreateAddress returns random address and corresponding private key
func CreateAddress() (types.Address, *ecdsa.PrivateKey) {
	pk, _ := crypto.GenerateKey()

	return crypto.PubkeyToAddress(pk.PublicKey), pk
}

// DefaultAppState returns new AppState with some predefined values
func DefaultAppState() types.AppState {
	return types.AppState{
		Note:                "",
		Validators:          nil,
		Candidates:          nil,
		BlockListCandidates: nil,
		Accounts:            nil,
		Coins:               nil,
		FrozenFunds:         nil,
		HaltBlocks:          nil,
		UsedChecks:          nil,
		MaxGas:              0,
		TotalSlashed:        "0",
	}
}
