package candidates

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	"github.com/MinterTeam/minter-go-node/rlp"
	"math/big"
	"sort"
	"sync"
)

// Deprecated
type candidateV1 struct {
	totalBipStake *big.Int
	stakesCount   int
	stakes        [MaxDelegatorsPerCandidate]*stake
	updates       []*stake
	tmAddress     *types.TmAddress
	lock          sync.RWMutex

	isDirty           bool
	isTotalStakeDirty bool
	isUpdatesDirty    bool
	dirtyStakes       [MaxDelegatorsPerCandidate]bool

	PubKey         types.Pubkey
	RewardAddress  types.Address
	OwnerAddress   types.Address
	ControlAddress types.Address
	Commission     uint32
	Status         byte
	ID             uint32
}

// Deprecated
func (c *Candidates) ExportV1toV2(state *types.AppState) {
	c.loadCandidatesDeliverV1()
	c.loadStakesV1()

	candidates := c.GetCandidates()
	state.Candidates = make([]types.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidateStakes := c.GetStakes(candidate.PubKey)
		stakes := make([]types.Stake, len(candidateStakes))
		for i, s := range candidateStakes {
			stakes[i] = types.Stake{
				Owner:    s.Owner,
				Coin:     uint64(s.Coin),
				Value:    s.Value.String(),
				BipValue: s.BipValue.String(),
			}
		}

		updates := make([]types.Stake, len(candidate.updates))
		for i, u := range candidate.updates {
			updates[i] = types.Stake{
				Owner:    u.Owner,
				Coin:     uint64(u.Coin),
				Value:    u.Value.String(),
				BipValue: u.BipValue.String(),
			}
		}

		state.Candidates = append(state.Candidates, types.Candidate{
			ID:             uint64(candidate.ID),
			RewardAddress:  candidate.RewardAddress,
			OwnerAddress:   candidate.OwnerAddress,
			ControlAddress: candidate.ControlAddress,
			TotalBipStake:  candidate.GetTotalBipStake().String(),
			PubKey:         candidate.PubKey,
			Commission:     uint64(candidate.Commission),
			Status:         uint64(candidate.Status),
			Updates:        updates,
			Stakes:         stakes,
		})
	}

	for pubkey := range c.blockList {
		state.BlockListCandidates = append(state.BlockListCandidates, pubkey)
	}
	sort.SliceStable(state.BlockListCandidates, func(i, j int) bool {
		return bytes.Compare(state.BlockListCandidates[i].Bytes(), state.BlockListCandidates[j].Bytes()) == 1
	})
}

// Deprecated
func (c *Candidates) loadCandidatesDeliverV1() {
	// if c.checkAndSetLoaded() {
	// 	return
	// }

	c.maxID = c.loadCandidatesListV1()

	_, blockListEnc := c.immutableTree().Get([]byte{blockListPrefix})
	if len(blockListEnc) != 0 {
		var blockList []types.Pubkey
		if err := rlp.DecodeBytes(blockListEnc, &blockList); err != nil {
			panic(fmt.Sprintf("failed to decode candidates block list: %s", err))
		}

		blockListMap := map[types.Pubkey]struct{}{}
		for _, pubkey := range blockList {
			blockListMap[pubkey] = struct{}{}
		}
		c.setBlockList(blockListMap)
	}

	_, valueMaxID := c.immutableTree().Get([]byte{maxIDPrefix})
	if len(valueMaxID) != 0 {
		c.maxID = binary.LittleEndian.Uint32(valueMaxID)
	}

}

// Deprecated
func (c *Candidates) loadCandidatesListV1() (maxID uint32) {
	_, pubIDenc := c.immutableTree().Get([]byte{pubKeyIDPrefix})
	if len(pubIDenc) != 0 {
		var pubIDs []pubkeyID
		if err := rlp.DecodeBytes(pubIDenc, &pubIDs); err != nil {
			panic(fmt.Sprintf("failed to decode candidates: %s", err))
		}

		pubKeyIDs := map[types.Pubkey]uint32{}
		for _, v := range pubIDs {
			pubKeyIDs[v.PubKey] = v.ID
			if v.ID > maxID {
				maxID = v.ID
			}
		}
		c.setPubKeyIDs(pubKeyIDs)
	}

	path := []byte{mainPrefix}
	_, enc := c.immutableTree().Get(path)
	if len(enc) != 0 {
		var candidatesV1 []*candidateV1
		if err := rlp.DecodeBytes(enc, &candidatesV1); err != nil {
			panic(fmt.Sprintf("failed to decode candidates: %s", err))
		}

		for _, candidateV1 := range candidatesV1 {
			candidate := &Candidate{
				PubKey:                   candidateV1.PubKey,
				RewardAddress:            candidateV1.RewardAddress,
				OwnerAddress:             candidateV1.OwnerAddress,
				ControlAddress:           candidateV1.ControlAddress,
				Commission:               candidateV1.Commission,
				Status:                   candidateV1.Status,
				ID:                       candidateV1.ID,
				LastEditCommissionHeight: 0, // THIS
			}
			// load total stake
			path = append([]byte{mainPrefix}, candidate.idBytes()...)
			path = append(path, totalStakePrefix)
			_, enc = c.immutableTree().Get(path)

			if len(enc) == 0 {
				candidate.totalBipStake = big.NewInt(0)
			} else {
				candidate.totalBipStake = big.NewInt(0).SetBytes(enc)
			}

			candidate.setTmAddress()
			c.setToMap(candidate.PubKey, candidate)
		}
	}

	return maxID
}

// Deprecated
func (c *Candidates) loadStakesV1() {
	for pubkey := range c.pubKeyIDs {
		c.loadStakesOfCandidateV1(pubkey)
	}
}

// Deprecated
func (c *Candidates) loadStakesOfCandidateV1(pubkey types.Pubkey) {
	candidate := c.GetCandidate(pubkey)

	// load stakes
	stakesCount := 0
	for index := 0; index < MaxDelegatorsPerCandidate; index++ {
		path := []byte{mainPrefix}
		path = append(path, candidate.idBytes()...)
		path = append(path, stakesPrefix)
		path = append(path, []byte(fmt.Sprintf("%d", index))...) // THIS
		_, enc := c.immutableTree().Get(path)
		if len(enc) == 0 {
			candidate.lock.Lock()
			candidate.stakes[index] = nil
			candidate.lock.Unlock()
			continue
		}

		stake := &stake{}
		if err := rlp.DecodeBytes(enc, stake); err != nil {
			panic(fmt.Sprintf("failed to decode stake: %s", err))
		}

		candidate.setStakeAtIndex(index, stake, false)

		stakesCount++
	}

	candidate.lock.Lock()
	candidate.stakesCount = stakesCount
	candidate.lock.Unlock()

	// load updates
	path := []byte{mainPrefix}
	path = append(path, candidate.idBytes()...)
	path = append(path, updatesPrefix)
	_, enc := c.immutableTree().Get(path)

	candidate.lock.Lock()
	if len(enc) == 0 {
		candidate.updates = nil
	} else {
		var updates []*stake
		if err := rlp.DecodeBytes(enc, &updates); err != nil {
			panic(fmt.Sprintf("failed to decode updated: %s", err))
		}

		for _, update := range updates {
			update.markDirty = (func(candidate *Candidate) func(int) {
				return func(i int) {
					candidate.lock.Lock()
					defer candidate.lock.Unlock()
					candidate.isUpdatesDirty = true
				}
			})(candidate)
		}

		candidate.updates = updates
	}
	candidate.lock.Unlock()

	// load total stake
	path = append([]byte{mainPrefix}, candidate.idBytes()...)
	path = append(path, totalStakePrefix)
	_, enc = c.immutableTree().Get(path)

	candidate.lock.Lock()
	if len(enc) == 0 {
		candidate.totalBipStake = big.NewInt(0)
	} else {
		candidate.totalBipStake = big.NewInt(0).SetBytes(enc)
	}
	candidate.lock.Unlock()

	candidate.setTmAddress()
	c.setToMap(candidate.PubKey, candidate)
}
