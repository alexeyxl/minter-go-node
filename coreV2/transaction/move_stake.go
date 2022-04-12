package transaction

import (
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/hexutil"
	"math/big"
	"strconv"

	"github.com/MinterTeam/minter-go-node/coreV2/code"
	"github.com/MinterTeam/minter-go-node/coreV2/state"
	"github.com/MinterTeam/minter-go-node/coreV2/state/commission"
	"github.com/MinterTeam/minter-go-node/coreV2/state/swap"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	abcTypes "github.com/tendermint/tendermint/abci/types"
)

type MoveStakeData struct {
	FromPubKey types.Pubkey
	ToPubKey   types.Pubkey
	Coin       types.CoinID
	Value      *big.Int
}

func (data MoveStakeData) TxType() TxType {
	return TypeMoveStake
}

func (data MoveStakeData) Gas() int64 {
	return gasMoveStake
}

func (data MoveStakeData) basicCheck(tx *Transaction, context *state.CheckState) *Response {
	if data.FromPubKey.Equals(data.ToPubKey) {
		return &Response{
			Code: code.EqualPubKey,
			Log:  fmt.Sprintf("Candidate \"FromPubKey\" equals candidate \"ToPubKey\": %s", data.FromPubKey),
			Info: EncodeError(code.NewEqualPubKey(data.ToPubKey.String())),
		}
	}

	if !context.Coins().Exists(data.Coin) {
		return &Response{
			Code: code.CoinNotExists,
			Log:  fmt.Sprintf("Coin %s not exists", data.Coin),
			Info: EncodeError(code.NewCoinNotExists("", data.Coin.String())),
		}
	}

	sender, _ := tx.Sender()

	var wlStake = new(big.Int)
	if waitlist := context.WaitList().Get(sender, data.FromPubKey, data.Coin); waitlist != nil {
		if data.Value.Cmp(waitlist.Value) != 1 {
			return nil
		}
		wlStake.Set(waitlist.Value)
	}

	if !context.Candidates().Exists(data.FromPubKey) {
		return &Response{
			Code: code.CandidateNotFound,
			Log:  "Candidate with such public key not found",
			Info: EncodeError(code.NewCandidateNotFound(data.FromPubKey.String())),
		}
	}

	stake := context.Candidates().GetStakeValueOfAddress(data.FromPubKey, sender, data.Coin)

	if stake != nil && stake.Sign() == 1 {
		wlStake.Add(wlStake, stake)
	} else if wlStake.Cmp(data.Value) < 0 {
		if wlStake.Sign() != 1 {
			return &Response{
				Code: code.StakeNotFound,
				Log:  "Stake of current user not found",
				Info: EncodeError(code.NewStakeNotFound(data.FromPubKey.String(), sender.String(), data.Coin.String(), context.Coins().GetCoin(data.Coin).GetFullSymbol())),
			}
		}
		return &Response{
			Code: code.InsufficientWaitList,
			Log:  "Insufficient amount at waitlist for sender account",
			Info: EncodeError(code.NewInsufficientWaitList(wlStake.String(), data.Value.String())),
		}
	}

	if wlStake.Cmp(data.Value) < 0 {
		return &Response{
			Code: code.InsufficientStake,
			Log:  "Insufficient stake for sender account",
			Info: EncodeError(code.NewInsufficientStake(data.FromPubKey.String(), sender.String(), data.Coin.String(), context.Coins().GetCoin(data.Coin).GetFullSymbol(), wlStake.String(), data.Value.String())),
		}
	}

	return nil
}

func (data MoveStakeData) String() string {
	return fmt.Sprintf("MOVE to pubkey:%s",
		hexutil.Encode(data.ToPubKey[:]))
}

func (data MoveStakeData) CommissionData(price *commission.Price) *big.Int {
	return price.MoveStake
}

func (data MoveStakeData) Run(tx *Transaction, context state.Interface, rewardPool *big.Int, currentBlock uint64, price *big.Int) Response {
	sender, _ := tx.Sender()
	var checkState *state.CheckState
	var isCheck bool
	if checkState, isCheck = context.(*state.CheckState); !isCheck {
		checkState = state.NewCheckState(context.(*state.State))
	}

	response := data.basicCheck(tx, checkState)
	if response != nil {
		return *response
	}

	commissionInBaseCoin := price
	commissionPoolSwapper := checkState.Swap().GetSwapper(tx.GasCoin, types.GetBaseCoinID())
	gasCoin := checkState.Coins().GetCoin(tx.GasCoin)
	commission, isGasCommissionFromPoolSwap, errResp := CalculateCommission(checkState, commissionPoolSwapper, gasCoin, commissionInBaseCoin)
	if errResp != nil {
		return *errResp
	}

	if checkState.Accounts().GetBalance(sender, tx.GasCoin).Cmp(commission) < 0 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission, gasCoin.GetFullSymbol()),
			Info: EncodeError(code.NewInsufficientFunds(sender.String(), commission.String(), gasCoin.GetFullSymbol(), gasCoin.ID().String())),
		}
	}

	var tags []abcTypes.EventAttribute
	if deliverState, ok := context.(*state.State); ok {
		// now + 7 days
		frozzToBlock := currentBlock + types.GetMovePeriod()

		var tagsCom *tagPoolChange
		if isGasCommissionFromPoolSwap {
			var (
				poolIDCom  uint32
				detailsCom *swap.ChangeDetailsWithOrders
				ownersCom  []*swap.OrderDetail
			)
			commission, commissionInBaseCoin, poolIDCom, detailsCom, ownersCom = deliverState.Swapper().PairSellWithOrders(tx.CommissionCoin(), types.GetBaseCoinID(), commission, big.NewInt(0))
			tagsCom = &tagPoolChange{
				PoolID:   poolIDCom,
				CoinIn:   tx.CommissionCoin(),
				ValueIn:  commission.String(),
				CoinOut:  types.GetBaseCoinID(),
				ValueOut: commissionInBaseCoin.String(),
				Orders:   detailsCom,
				// Sellers:  ownersCom,
			}
			for _, value := range ownersCom {
				deliverState.Accounts.AddBalance(value.Owner, tx.CommissionCoin(), value.ValueBigInt)
			}
		} else if !tx.GasCoin.IsBaseCoin() {
			deliverState.Coins.SubVolume(tx.CommissionCoin(), commission)
			deliverState.Coins.SubReserve(tx.CommissionCoin(), commissionInBaseCoin)
		}
		deliverState.Accounts.SubBalance(sender, tx.GasCoin, commission)
		rewardPool.Add(rewardPool, commissionInBaseCoin)

		if waitList := deliverState.Waitlist.Get(sender, data.FromPubKey, data.Coin); waitList != nil {
			diffValue := big.NewInt(0).Sub(data.Value, waitList.Value)
			deliverState.Waitlist.Delete(sender, data.FromPubKey, data.Coin)
			switch diffValue.Sign() {
			case -1:
				deliverState.Waitlist.AddWaitList(sender, data.FromPubKey, data.Coin, big.NewInt(0).Neg(diffValue))
			case 1:
				deliverState.Candidates.SubStake(sender, data.FromPubKey, data.Coin, diffValue)
			default:
			}
		} else {
			deliverState.Candidates.SubStake(sender, data.FromPubKey, data.Coin, data.Value)
		}
		deliverState.FrozenFunds.AddFund(frozzToBlock, sender, &data.FromPubKey, deliverState.Candidates.ID(data.FromPubKey), data.Coin, data.Value, deliverState.Candidates.ID(data.ToPubKey))

		deliverState.Accounts.SetNonce(sender, tx.Nonce)

		tags = []abcTypes.EventAttribute{
			{Key: []byte("tx.commission_in_base_coin"), Value: []byte(commissionInBaseCoin.String())},
			{Key: []byte("tx.commission_conversion"), Value: []byte(isGasCommissionFromPoolSwap.String()), Index: true},
			{Key: []byte("tx.commission_amount"), Value: []byte(commission.String())},
			{Key: []byte("tx.commission_details"), Value: []byte(tagsCom.string())},
			{Key: []byte("tx.public_key"), Value: []byte(hex.EncodeToString(data.FromPubKey[:])), Index: true},
			{Key: []byte("tx.to_public_key"), Value: []byte(hex.EncodeToString(data.ToPubKey[:])), Index: true},
			{Key: []byte("tx.coin_id"), Value: []byte(data.Coin.String()), Index: true},
			{Key: []byte("tx.unlock_block_id"), Value: []byte(strconv.Itoa(int(frozzToBlock)))},
		}
	}

	return Response{
		Code: code.OK,
		Tags: tags,
	}
}
