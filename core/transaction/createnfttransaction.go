// Copyright (c) 2017-2021 The Elastos Foundation
// Use of this source code is governed by an MIT
// license that can be found in the LICENSE file.
//

package transaction

import (
	"errors"
	"fmt"

	"github.com/elastos/Elastos.ELA/core/types/payload"
	elaerr "github.com/elastos/Elastos.ELA/errors"
)

type CreateNFTTransaction struct {
	BaseTransaction
}

func (t *CreateNFTTransaction) CheckTransactionPayload() error {
	switch t.Payload().(type) {
	case *payload.CreateNFT:
		return nil
	}

	return errors.New("invalid payload type")
}

func (t *CreateNFTTransaction) IsAllowedInPOWConsensus() bool {
	return false
}

func (t *CreateNFTTransaction) HeightVersionCheck() error {
	blockHeight := t.parameters.BlockHeight
	chainParams := t.parameters.Config

	if blockHeight < chainParams.NFTStartHeight {
		return errors.New(fmt.Sprintf("not support %s transaction "+
			"before NFTStartHeight", t.TxType().Name()))
	}
	return nil
}

func (t *CreateNFTTransaction) SpecialContextCheck() (elaerr.ELAError, bool) {
	pld, ok := t.Payload().(*payload.CreateNFT)
	if !ok {
		return elaerr.Simple(elaerr.ErrTxPayload, errors.New("invalid payload")), true
	}

	state := t.parameters.BlockChain.GetState()
	producers := state.GetDposV2Producers()
	var exist bool
	for _, p := range producers {
		for _, votesInfo := range p.GetAllDetailedDPoSV2Votes() {
			for referKey, voteInfo := range votesInfo {
				if referKey.IsEqual(pld.ID) {
					exist = true
					log.Info("create NFT, vote information:", voteInfo)
					break
				}
			}
			if exist {
				break
			}
		}
		if exist {
			break
		}
	}

	if !exist {
		return elaerr.Simple(elaerr.ErrTxPayload, errors.New("the NFT ID does not exist")), true
	}

	return nil, false
}
