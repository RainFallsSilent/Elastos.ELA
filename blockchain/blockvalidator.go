// Copyright (c) 2017-2020 The Elastos Foundation
// Use of this source code is governed by an MIT
// license that can be found in the LICENSE file.
//

package blockchain

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"strconv"
	"time"

	. "github.com/elastos/Elastos.ELA/auxpow"
	. "github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/common/config"
	"github.com/elastos/Elastos.ELA/common/log"
	"github.com/elastos/Elastos.ELA/core"
	. "github.com/elastos/Elastos.ELA/core/types"
	"github.com/elastos/Elastos.ELA/core/types/common"
	"github.com/elastos/Elastos.ELA/core/types/interfaces"
	"github.com/elastos/Elastos.ELA/core/types/payload"
	"github.com/elastos/Elastos.ELA/crypto"
	"github.com/elastos/Elastos.ELA/dpos/state"
	"github.com/elastos/Elastos.ELA/elanet/pact"
	elaerr "github.com/elastos/Elastos.ELA/errors"
)

const (
	MaxTimeOffsetSeconds = 2 * 60 * 60
)

func (b *BlockChain) CheckBlockSanity(block *Block) error {
	header := block.Header
	hash := header.Hash()
	if !header.AuxPow.Check(&hash, AuxPowChainID) {
		return errors.New("[PowCheckBlockSanity] block check aux pow failed")
	}
	if CheckProofOfWork(&header, b.chainParams.PowConfiguration.PowLimit) != nil {
		return errors.New("[PowCheckBlockSanity] block check proof of work failed")
	}

	// A block timestamp must not have a greater precision than one second.
	tempTime := time.Unix(int64(header.Timestamp), 0)
	if !tempTime.Equal(time.Unix(tempTime.Unix(), 0)) {
		return errors.New("[PowCheckBlockSanity] block timestamp of has a higher precision than one second")
	}

	// Ensure the block time is not too far in the future.
	maxTimestamp := b.TimeSource.AdjustedTime().Add(time.Second * MaxTimeOffsetSeconds)
	if tempTime.After(maxTimestamp) {
		return errors.New("[PowCheckBlockSanity] block timestamp of is too far in the future")
	}

	// A block must have at least one transaction.
	numTx := len(block.Transactions)
	if numTx == 0 {
		return errors.New("[PowCheckBlockSanity]  block does not contain any transactions")
	}

	// A block must not have more transactions than the max block payload.
	if uint32(numTx) > pact.MaxTxPerBlock {
		return errors.New("[PowCheckBlockSanity]  block contains too many" +
			" transactions, tx count: " + strconv.FormatInt(int64(numTx), 10))
	}

	// A block header must not exceed the maximum allowed block payload when
	//serialized.
	headerSize := block.Header.GetSize()
	if headerSize > int(pact.MaxBlockHeaderSize) {
		return errors.New(
			"[PowCheckBlockSanity] serialized block header is too big")
	}

	// A block must not exceed the maximum allowed block payload when serialized.
	blockSize := block.GetSize()
	if blockSize > int(pact.MaxBlockContextSize+pact.MaxBlockHeaderSize) {
		return errors.New("[PowCheckBlockSanity] serialized block is too big")
	}

	transactions := block.Transactions
	// The first transaction in a block must be a coinbase.
	if !transactions[0].IsCoinBaseTx() {
		return errors.New("[PowCheckBlockSanity] first transaction in block is not a coinbase")
	}

	// A block must not have more than one coinbase.
	for _, tx := range transactions[1:] {
		if tx.IsCoinBaseTx() {
			return errors.New("[PowCheckBlockSanity] block contains second coinbase")
		}
	}

	txIDs := make([]Uint256, 0, len(block.Transactions))
	existingTxIDs := make(map[Uint256]struct{})
	existingTxInputs := make(map[string]struct{})
	for _, txn := range block.Transactions {
		txID := txn.Hash()
		// Check for duplicate transactions.
		if _, exists := existingTxIDs[txID]; exists {
			return errors.New("[PowCheckBlockSanity] block contains duplicate transaction")
		}
		existingTxIDs[txID] = struct{}{}

		// Check for transaction sanity
		if err := b.CheckTransactionSanity(block.Height, txn); err != nil {
			return elaerr.SimpleWithMessage(elaerr.ErrBlockValidation, err,
				"CheckTransactionSanity failed when verifiy block")
		}

		// Check for duplicate UTXO Inputs in a block
		for _, input := range txn.Inputs() {
			referKey := input.ReferKey()
			if _, exists := existingTxInputs[referKey]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate UTXO")
			}
			existingTxInputs[referKey] = struct{}{}
		}

		// Append transaction to list
		txIDs = append(txIDs, txID)
	}
	if err := CheckDuplicateTx(block); err != nil {
		return err
	}
	calcTransactionsRoot, err := crypto.ComputeRoot(txIDs)
	if err != nil {
		return errors.New("[PowCheckBlockSanity] merkleTree compute failed")
	}
	if !header.MerkleRoot.IsEqual(calcTransactionsRoot) {
		return errors.New("[PowCheckBlockSanity] block merkle root is invalid")
	}

	return nil
}

func CheckDuplicateTx(block *Block) error {
	existingSideTxs := make(map[Uint256]struct{})
	existingProducer := make(map[string]struct{})
	existingProducerNode := make(map[string]struct{})
	existingCR := make(map[Uint168]struct{})
	recordSponsorCount := 0
	for _, txn := range block.Transactions {
		switch txn.TxType() {
		case common.RecordSponsor:
			recordSponsorCount++
			if recordSponsorCount > 1 {
				return errors.New("[PowCheckBlockSanity] block contains duplicate record sponsor Tx")
			}

		case common.WithdrawFromSideChain:
			witPayload := txn.Payload().(*payload.WithdrawFromSideChain)

			// Check for duplicate sidechain tx in a block
			for _, hash := range witPayload.SideChainTransactionHashes {
				if _, exists := existingSideTxs[hash]; exists {
					return errors.New("[PowCheckBlockSanity] block contains duplicate sidechain Tx")
				}
				existingSideTxs[hash] = struct{}{}
			}
		case common.RegisterProducer:
			producerPayload, ok := txn.Payload().(*payload.ProducerInfo)
			if !ok {
				return errors.New("[PowCheckBlockSanity] invalid register producer payload")
			}

			producer := BytesToHexString(producerPayload.OwnerKey)
			// Check for duplicate producer in a block
			if _, exists := existingProducer[producer]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate producer")
			}
			existingProducer[producer] = struct{}{}

			producerNode := BytesToHexString(producerPayload.NodePublicKey)
			// Check for duplicate producer node in a block
			if _, exists := existingProducerNode[producerNode]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate producer node")
			}
			existingProducerNode[producerNode] = struct{}{}
		case common.UpdateProducer:
			producerPayload, ok := txn.Payload().(*payload.ProducerInfo)
			if !ok {
				return errors.New("[PowCheckBlockSanity] invalid update producer payload")
			}

			producer := BytesToHexString(producerPayload.OwnerKey)
			// Check for duplicate producer in a block
			if _, exists := existingProducer[producer]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate producer")
			}
			existingProducer[producer] = struct{}{}

			producerNode := BytesToHexString(producerPayload.NodePublicKey)
			// Check for duplicate producer node in a block
			if _, exists := existingProducerNode[BytesToHexString(producerPayload.NodePublicKey)]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate producer node")
			}
			existingProducerNode[producerNode] = struct{}{}
		case common.CancelProducer:
			processProducerPayload, ok := txn.Payload().(*payload.ProcessProducer)
			if !ok {
				return errors.New("[PowCheckBlockSanity] invalid cancel producer payload")
			}

			producer := BytesToHexString(processProducerPayload.OwnerKey)
			// Check for duplicate producer in a block
			if _, exists := existingProducer[producer]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate producer")
			}
			existingProducer[producer] = struct{}{}
		case common.RegisterCR:
			crPayload, ok := txn.Payload().(*payload.CRInfo)
			if !ok {
				return errors.New("[PowCheckBlockSanity] invalid register CR payload")
			}

			// Check for duplicate CR in a block
			if _, exists := existingCR[crPayload.CID]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate CR")
			}
			existingCR[crPayload.CID] = struct{}{}
		case common.UpdateCR:
			crPayload, ok := txn.Payload().(*payload.CRInfo)
			if !ok {
				return errors.New("[PowCheckBlockSanity] invalid update CR payload")
			}

			// Check for duplicate  CR in a block
			if _, exists := existingCR[crPayload.CID]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate CR")
			}
			existingCR[crPayload.CID] = struct{}{}
		case common.UnregisterCR:
			unregisterCR, ok := txn.Payload().(*payload.UnregisterCR)
			if !ok {
				return errors.New("[PowCheckBlockSanity] invalid unregister CR payload")
			}
			// Check for duplicate  CR in a block
			if _, exists := existingCR[unregisterCR.CID]; exists {
				return errors.New("[PowCheckBlockSanity] block contains duplicate CR")
			}
			existingCR[unregisterCR.CID] = struct{}{}
		}
	}
	return nil
}

func RecordCRCProposalAmount(usedAmount *Fixed64, txn interfaces.Transaction) {
	proposal, ok := txn.Payload().(*payload.CRCProposal)
	if !ok {
		return
	}
	for _, b := range proposal.Budgets {
		*usedAmount += b.Amount
	}
}

func (b *BlockChain) checkTxsContext(block *Block) error {
	var totalTxFee = Fixed64(0)

	var proposalsUsedAmount Fixed64
	for i := 1; i < len(block.Transactions); i++ {
		references, errCode := b.CheckTransactionContext(block.Height,
			block.Transactions[i], proposalsUsedAmount, block.Timestamp)
		if errCode != nil {
			return elaerr.SimpleWithMessage(elaerr.ErrBlockValidation, errCode,
				"CheckTransactionContext failed when verify block")
		}

		// Calculate transaction fee
		totalTxFee += GetTxFee(block.Transactions[i], core.ELAAssetID, references)
		if block.Transactions[i].IsCRCProposalTx() {
			RecordCRCProposalAmount(&proposalsUsedAmount, block.Transactions[i])
		}
	}
	dposReward := b.GetBlockDPOSReward(block)
	err := b.checkCoinbaseTransactionContext(block.Height,
		block.Transactions[0], totalTxFee, dposReward)
	if err != nil {
		buf := new(bytes.Buffer)
		if block.Height < b.chainParams.CheckRewardHeight {
			if err = block.Serialize(buf); err != nil {
				return err
			}
		} else {
			if e := block.Serialize(buf); e != nil {
				return e
			}
		}
		log.Errorf("checkCoinbaseTransactionContext failed,"+
			"block:%s", BytesToHexString(buf.Bytes()))
		log.Error("checkCoinbaseTransactionContext failed,round reward:",
			DefaultLedger.Arbitrators.GetArbitersRoundReward())
		log.Error("checkCoinbaseTransactionContext failed,final round change:",
			DefaultLedger.Arbitrators.GetFinalRoundChange())
	}
	return err
}

func (b *BlockChain) CheckBlockContext(block *Block, prevNode *BlockNode) error {
	// The genesis block is valid by definition.
	if prevNode == nil {
		return nil
	}

	header := block.Header
	expectedDifficulty, err := b.CalcNextRequiredDifficulty(prevNode,
		time.Unix(int64(header.Timestamp), 0))
	if err != nil {
		return err
	}

	if header.Bits != expectedDifficulty {
		return errors.New("block difficulty is not the expected")
	}

	// Ensure the timestamp for the block header is after the
	// median time of the last several blocks (medianTimeBlocks).
	medianTime := CalcPastMedianTime(prevNode)
	tempTime := time.Unix(int64(header.Timestamp), 1)

	if !tempTime.After(medianTime) {
		return errors.New("block timestamp is not after expected")
	}

	var recordSponsorExist bool
	for _, tx := range block.Transactions[1:] {
		if !IsFinalizedTransaction(tx, block.Height) {
			return errors.New("block contains unfinalized transaction")
		}
		if tx.IsRecordSponorTx() {
			recordSponsorExist = true
		}
	}

	// check if need to record sponsor
	if block.Height >= b.chainParams.DPoSConfiguration.RecordSponsorStartHeight {
		lastBlock, err := b.GetDposBlockByHash(*prevNode.Hash)
		if err != nil {
			// try get block from cache
			lastBlockInCache, ok := b.blockCache[*prevNode.Hash]
			if !ok {
				return errors.New("get last block failed")
			}
			lastConfirmInCache, ok := b.confirmCache[*prevNode.Hash]
			if !ok {
				return errors.New("get last block confirm failed")
			}
			lastBlock = &DposBlock{
				Block:       lastBlockInCache,
				HaveConfirm: lastConfirmInCache != nil,
				Confirm:     lastConfirmInCache,
			}
		}

		if lastBlock.Confirm == nil && recordSponsorExist {
			return errors.New("record sponsor transaction must be confirmed")
		}
		if lastBlock.Confirm != nil && !recordSponsorExist {
			return errors.New("confirmed block must have record sponsor transaction")
		}
	}

	if err := DefaultLedger.Arbitrators.CheckDPOSIllegalTx(block); err != nil {
		return err
	}

	if err := DefaultLedger.Arbitrators.CheckCRCAppropriationTx(block); err != nil {
		return err
	}
	if err := DefaultLedger.Arbitrators.CheckNextTurnDPOSInfoTx(block); err != nil {
		return err
	}
	if err := DefaultLedger.Arbitrators.CheckCustomIDResultsTx(block); err != nil {
		return err
	}
	return b.checkTxsContext(block)
}

func (b *BlockChain) CheckTransactions(block *Block) error {
	if err := DefaultLedger.Arbitrators.CheckNextTurnDPOSInfoTx(block); err != nil {
		return err
	}

	return nil
}

func CheckProofOfWork(header *common.Header, powLimit *big.Int) error {
	// The target difficulty must be larger than zero.
	target := CompactToBig(header.Bits)
	if target.Sign() <= 0 {
		return errors.New("[BlockValidator], block target difficulty is too low.")
	}

	// The target difficulty must be less than the maximum allowed.
	if target.Cmp(powLimit) > 0 {
		return errors.New("[BlockValidator], block target difficulty is higher than max of limit.")
	}

	// The block hash must be less than the claimed target.
	hash := header.AuxPow.ParBlockHeader.Hash()

	hashNum := HashToBig(&hash)
	if hashNum.Cmp(target) > 0 {
		return errors.New("[BlockValidator], block target difficulty is higher than expected difficulty.")
	}

	return nil
}

func IsFinalizedTransaction(msgTx interfaces.Transaction, blockHeight uint32) bool {
	// Lock time of zero means the transaction is finalized.
	lockTime := msgTx.LockTime()
	if lockTime == 0 {
		return true
	}

	//FIXME only height
	if lockTime < blockHeight {
		return true
	}

	// At this point, the transaction's lock time hasn't occurred yet, but
	// the transaction might still be finalized if the sequence number
	// for all transaction Inputs is maxed out.
	for _, txIn := range msgTx.Inputs() {
		if txIn.Sequence != math.MaxUint16 {
			return false
		}
	}
	return true
}

func GetTxFee(tx interfaces.Transaction, assetId Uint256, references map[*common.Input]common.Output) Fixed64 {
	feeMap, err := GetTxFeeMap(tx, references)
	if err != nil {
		return 0
	}

	return feeMap[assetId]
}

func GetTxFeeMap(tx interfaces.Transaction, references map[*common.Input]common.Output) (map[Uint256]Fixed64, error) {
	feeMap := make(map[Uint256]Fixed64)
	var inputs = make(map[Uint256]Fixed64)
	var outputs = make(map[Uint256]Fixed64)

	for _, output := range references {
		amount, ok := inputs[output.AssetID]
		if ok {
			inputs[output.AssetID] = amount + output.Value
		} else {
			inputs[output.AssetID] = output.Value
		}
	}
	for _, v := range tx.Outputs() {
		amount, ok := outputs[v.AssetID]
		if ok {
			outputs[v.AssetID] = amount + v.Value
		} else {
			outputs[v.AssetID] = v.Value
		}
	}

	//calc the balance of input vs output
	for outputAssetid, outputValue := range outputs {
		if inputValue, ok := inputs[outputAssetid]; ok {
			feeMap[outputAssetid] = inputValue - outputValue
		} else {
			feeMap[outputAssetid] -= outputValue
		}
	}
	for inputAssetId, inputValue := range inputs {
		if _, exist := feeMap[inputAssetId]; !exist {
			feeMap[inputAssetId] += inputValue
		}
	}

	return feeMap, nil
}

func (b *BlockChain) GetBlockDPOSReward(block *Block) Fixed64 {
	totalTxFx := Fixed64(0)
	for _, tx := range block.Transactions {
		totalTxFx += tx.Fee()
	}
	return Fixed64(math.Ceil(float64(totalTxFx+
		b.chainParams.GetBlockReward(block.Height)) * 0.35))
}

func (b *BlockChain) checkCoinbaseTransactionContext(blockHeight uint32, coinbase interfaces.Transaction, totalTxFee, dposReward Fixed64) error {
	activeHeight := DefaultLedger.Arbitrators.GetDPoSV2ActiveHeight()
	if activeHeight != math.MaxUint32 && blockHeight > activeHeight+1 {
		totalReward := totalTxFee + b.chainParams.GetBlockReward(blockHeight)
		rewardCyberRepublic := Fixed64(math.Ceil(float64(totalReward) * 0.3))
		rewardDposArbiter := Fixed64(math.Ceil(float64(totalReward) * 0.35))
		rewardMergeMiner := Fixed64(totalReward) - rewardCyberRepublic - rewardDposArbiter
		if coinbase.Outputs()[0].Value != rewardCyberRepublic {
			return errors.New("rewardCyberRepublic value not correct")
		}
		if coinbase.Outputs()[1].Value != rewardMergeMiner {
			return errors.New("rewardMergeMiner value not correct")
		}
		if len(coinbase.Outputs()) != 3 {
			return errors.New("coinbase only can have 3 outputs at the most when it is DPoS v2")
		}
		if coinbase.Outputs()[2].Value != dposReward {
			return errors.New("last DPoS reward value not correct")
		}

		if b.state.GetConsensusAlgorithm() == state.POW {
			if !coinbase.Outputs()[2].ProgramHash.IsEqual(*b.chainParams.DestroyELAProgramHash) {
				return errors.New("DPoS reward address not correct")
			}
			if !coinbase.Outputs()[0].ProgramHash.IsEqual(*b.chainParams.DestroyELAProgramHash) {
				return errors.New("rewardCyberRepublic address not correct")
			}
		} else {
			if !coinbase.Outputs()[0].ProgramHash.IsEqual(*b.chainParams.CRConfiguration.CRAssetsProgramHash) {
				return errors.New("rewardCyberRepublic address not correct")
			}
			if !coinbase.Outputs()[2].ProgramHash.IsEqual(*b.chainParams.DPoSConfiguration.DPoSV2RewardAccumulateProgramHash) {
				return errors.New("DPoS reward address not correct")
			}
		}

		return nil
	}

	// main version >= H2
	if blockHeight >= b.chainParams.PublicDPOSHeight {
		totalReward := totalTxFee + b.chainParams.GetBlockReward(blockHeight)
		rewardDPOSArbiter := Fixed64(math.Ceil(float64(totalReward) * 0.35))
		if totalReward-rewardDPOSArbiter+DefaultLedger.Arbitrators.
			GetFinalRoundChange() != coinbase.Outputs()[0].Value+
			coinbase.Outputs()[1].Value {

			return errors.New("reward amount in coinbase not correct")
		}

		if err := CheckCoinbaseArbitratorsReward(coinbase); err != nil {
			return err
		}
	} else { // old version [0, H2)
		var rewardInCoinbase = Fixed64(0)
		for _, output := range coinbase.Outputs() {
			rewardInCoinbase += output.Value
		}

		// Reward in coinbase must match inflation 4% per year
		if rewardInCoinbase-totalTxFee != b.chainParams.GetBlockReward(blockHeight) {
			return errors.New("Reward amount in coinbase not correct, " +
				"height:" + strconv.FormatUint(uint64(blockHeight),
				10) + "dposheight: " + strconv.FormatUint(uint64(config.
				DefaultParams.PublicDPOSHeight), 10))
		}
	}

	return nil
}

func CheckCoinbaseArbitratorsReward(coinbase interfaces.Transaction) error {
	rewards := DefaultLedger.Arbitrators.GetArbitersRoundReward()
	if len(rewards) != len(coinbase.Outputs())-2 {
		return errors.New("coinbase output count not match")
	}

	for i := 2; i < len(coinbase.Outputs()); i++ {
		amount, ok := rewards[coinbase.Outputs()[i].ProgramHash]
		if !ok {
			return errors.New("unknown dpos reward address")
		}
		if amount != coinbase.Outputs()[i].Value {
			return errors.New("incorrect dpos reward amount")
		}
	}

	return nil
}
