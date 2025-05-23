// Copyright (c) 2017-2021 The Elastos Foundation
// Use of this source code is governed by an MIT
// license that can be found in the LICENSE file.
//

package transaction

import (
	"errors"
	"fmt"

	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/core"
	"github.com/elastos/Elastos.ELA/core/contract"
	program2 "github.com/elastos/Elastos.ELA/core/contract/program"
	common2 "github.com/elastos/Elastos.ELA/core/types/common"
	"github.com/elastos/Elastos.ELA/core/types/outputpayload"
	"github.com/elastos/Elastos.ELA/core/types/payload"
	"github.com/elastos/Elastos.ELA/dpos/state"
	elaerr "github.com/elastos/Elastos.ELA/errors"
)

type ExchangeVotesTransaction struct {
	BaseTransaction
}

func (t *ExchangeVotesTransaction) HeightVersionCheck() error {
	blockHeight := t.parameters.BlockHeight
	chainParams := t.parameters.Config

	if blockHeight < chainParams.DPoSV2StartHeight {
		log.Debug("blockHeight:", blockHeight, "DPoSV2StartHeight:", chainParams.DPoSV2StartHeight)
		return errors.New(fmt.Sprintf("not support %s transaction "+
			"before DPoSV2StartHeight", t.TxType().Name()))
	}

	if blockHeight < chainParams.MultiExchangeVotesStartHeight &&
		len(t.programs) > 1 {
		return errors.New(fmt.Sprintf("not support multi-addr %s transaction "+
			"before MultiExchangeVotesStartHeight", t.TxType().Name()))
	}
	return nil
}

// pow or  before MultiExchangeVotesStartHeight
func (t *ExchangeVotesTransaction) CheckOutputSingleInput() error {
	if len(t.Outputs()) > 2 {
		return errors.New("output count should not be greater than 2")
	}

	if len(t.Outputs()) < 1 {
		return errors.New("transaction has no outputs")
	}

	if len(t.Programs()) != 1 {
		return errors.New("invalid programs count")
	}
	// check if output address is valid
	for _, output := range t.Outputs() {
		if output.AssetID != core.ELAAssetID {
			return errors.New("asset ID in output is invalid")
		}

		// output value must > 0
		if output.Value <= common.Fixed64(0) {
			return errors.New("invalid transaction UTXO output")
		}
	}

	// check output payload
	if t.outputs[0].Type != common2.OTStake {
		return errors.New("invalid output type")
	}
	p := t.outputs[0].Payload
	if p == nil {
		return errors.New("invalid output payload")
	}
	sopayload, ok := p.(*outputpayload.ExchangeVotesOutput)
	if !ok {
		return errors.New("invalid exchange vote output payload")
	}
	if err := p.Validate(); err != nil {
		return err
	}

	// check output[0] stake address
	code := t.Programs()[0].Code
	ct, err := contract.CreateStakeContractByCode(code)
	if err != nil {
		return errors.New("invalid code")
	}
	stakeProgramHash := ct.ToProgramHash()

	if !stakeProgramHash.IsEqual(sopayload.StakeAddress) {
		return errors.New("invalid stake address")
	}

	// check output address, need to be stake pool
	if t.outputs[0].ProgramHash != *t.parameters.Config.StakePoolProgramHash {
		return errors.New("first output address need to be stake address")
	}

	// check the second output
	if len(t.Outputs()) == 2 {
		if contract.GetPrefixType(t.Outputs()[1].ProgramHash) != contract.PrefixStandard &&
			contract.GetPrefixType(t.Outputs()[1].ProgramHash) != contract.PrefixMultiSig {
			return errors.New("second output address need to be Standard or MultiSig")
		}
	}

	return nil
}

func (t *ExchangeVotesTransaction) CheckOutputMultiInputs() error {
	if len(t.Outputs()) < 1 {
		return errors.New("transaction has no outputs")
	}

	if len(t.Programs()) < 1 {
		return errors.New("invalid programs count")
	}
	// check if output address is valid
	for i, output := range t.Outputs() {
		if output.AssetID != core.ELAAssetID {
			return errors.New("asset ID in output is invalid")
		}

		// output value must > 0
		if output.Value <= common.Fixed64(0) {
			return errors.New("invalid transaction UTXO output")
		}
		if i >= 1 {
			if contract.GetPrefixType(output.ProgramHash) != contract.PrefixStandard &&
				contract.GetPrefixType(output.ProgramHash) != contract.PrefixMultiSig {
				return errors.New("second output address need to be Standard or MultiSig")
			}
		}
	}

	// check output payload
	if t.outputs[0].Type != common2.OTStake {
		return errors.New("invalid output type")
	}
	p := t.outputs[0].Payload
	if p == nil {
		return errors.New("invalid output payload")
	}
	if err := p.Validate(); err != nil {
		return err
	}

	// check output address, need to be stake pool
	if t.outputs[0].ProgramHash != *t.parameters.Config.StakePoolProgramHash {
		return errors.New("first output address need to be stake address")
	}

	return nil
}

func (t *ExchangeVotesTransaction) CheckTransactionOutput() error {
	inPow := t.parameters.BlockChain.GetState().GetConsensusAlgorithm() == state.POW
	if inPow || t.parameters.BlockHeight < t.parameters.Config.MultiExchangeVotesStartHeight {
		return t.CheckOutputSingleInput()
	} else {
		return t.CheckOutputMultiInputs()
	}
}

func (t *ExchangeVotesTransaction) CheckTransactionPayload() error {
	switch t.Payload().(type) {
	case *payload.ExchangeVotes:
		return nil
	}

	return errors.New("invalid payload type")
}

func (t *ExchangeVotesTransaction) CheckAttributeProgram() error {
	// Check attributes
	for _, attr := range t.Attributes() {
		if !common2.IsValidAttributeType(attr.Usage) {
			return fmt.Errorf("invalid attribute usage %v", attr.Usage)
		}
	}
	if t.parameters.BlockChain.GetState().GetConsensusAlgorithm() == state.POW {
		if len(t.Programs()) != 1 {
			return errors.New("transaction should have one  program")
		}
	} else {
		// Check programs
		if len(t.Programs()) < 1 {
			return errors.New("transaction should have program")
		}
	}

	for _, p := range t.Programs() {
		if p.Code == nil {
			return fmt.Errorf("invalid program code nil")
		}
		if len(p.Code) < program2.MinProgramCodeSize {
			return fmt.Errorf("invalid program code size")
		}
		if p.Parameter == nil {
			return fmt.Errorf("invalid program parameter nil")
		}
	}

	return nil
}

func (t *ExchangeVotesTransaction) IsAllowedInPOWConsensus() bool {

	return true
}

func (t *ExchangeVotesTransaction) SpecialContextCheck() (result elaerr.ELAError, end bool) {

	if t.parameters.BlockHeight < t.parameters.Config.VotesSchnorrStartHeight {
		for _, program := range t.programs {
			if contract.IsSchnorr(program.Code) {
				return elaerr.Simple(elaerr.ErrTxPayload,
					errors.New(fmt.Sprintf("not support %s transaction "+
						"before VotesSchnorrStartHeight:", t.TxType().Name()))), true
			}
		}
	}
	return nil, false
}
