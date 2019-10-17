// Copyright (c) 2017-2019 The Elastos Foundation
// Use of this source code is governed by an MIT
// license that can be found in the LICENSE file.
//

package state

import (
	"sync"

	"github.com/elastos/Elastos.ELA/common"
	"github.com/elastos/Elastos.ELA/common/config"
	"github.com/elastos/Elastos.ELA/core/contract"
	"github.com/elastos/Elastos.ELA/core/types"
	"github.com/elastos/Elastos.ELA/core/types/payload"
	"github.com/elastos/Elastos.ELA/utils"
)

type ProposalStatus uint8

const (
	// Registered is the status means the CRC proposal tx has been on the best
	//	chain.
	Registered ProposalStatus = 0x00

	// CRAgreed means CRC has agreed the proposal.
	CRAgreed ProposalStatus = 0x01

	// VoterAgreed means there are not enough negative vote about the proposal.
	VoterAgreed ProposalStatus = 0x02

	// Finished means the proposal has run out the lifetime.
	Finished ProposalStatus = 0x03

	// CRCanceled means the proposal canceled by CRC voting.
	CRCanceled ProposalStatus = 0x04

	// VoterCanceled means the proposal canceled by voters' reject voting.
	VoterCanceled ProposalStatus = 0x05

	// Aborted means proposal had been approved by both CR and voters,
	// whoever the proposal related project has been decided to abort for
	// some reason.
	Aborted ProposalStatus = 0x06
)

// ProposalManager used to manage all proposals existing in block chain.
type ProposalManager struct {
	ProposalKeyFrame
	params *config.Params
	mtx    sync.Mutex
}

// ExistDraft judge if specified draft (that related to a proposal) exist.
func (p *ProposalManager) ExistDraft(hash common.Uint256) bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	for _, v := range p.Proposals {
		if v.Proposal.DraftHash.IsEqual(hash) {
			return true
		}
	}
	return false
}

// ExistProposal judge if specified proposal exist.
func (p *ProposalManager) ExistProposal(hash common.Uint256) bool {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	_, ok := p.Proposals[hash]
	return ok
}

// GetProposal will return a proposal with specified hash,
// and return nil if not found.
func (p *ProposalManager) GetProposal(hash common.Uint256) *ProposalState {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	return p.getProposal(hash)
}

// getProposal will return a proposal with specified hash,
// and return nil if not found.
func (p *ProposalManager) getProposal(hash common.Uint256) *ProposalState {
	result, ok := p.Proposals[hash]
	if !ok {
		return nil
	}
	return result
}

// updateProposals will update proposals' status.
func (p *ProposalManager) updateProposals(height uint32,
	history *utils.History) {
	for k, v := range p.Proposals {
		switch v.Status {
		case Registered:
			if p.shouldEndCRCVote(k, height) {
				p.transferRegisteredState(v, height, history)
			}
		case CRAgreed:
			if p.shouldEndPublicVote(k, height) {
				p.transferCRAgreedState(v, height, history)
			}
		}
	}
}

// transferRegisteredState will transfer the Registered state by CR agreement
// count.
func (p *ProposalManager) transferRegisteredState(proposal *ProposalState,
	height uint32, history *utils.History) {
	agreedCount := uint32(0)
	for _, v := range proposal.CRVotes {
		if v == payload.Approve {
			agreedCount++
		}
	}

	if agreedCount >= p.params.CRAgreementCount {
		history.Append(height, func() {
			proposal.Status = CRAgreed
		}, func() {
			proposal.Status = Registered
		})
	} else {
		history.Append(height, func() {
			proposal.Status = CRCanceled
		}, func() {
			proposal.Status = Registered
		})
	}
}

// transferCRAgreedState will transfer CRAgreed state by votes' reject amount.
func (p *ProposalManager) transferCRAgreedState(proposal *ProposalState,
	height uint32, history *utils.History) {
	// todo get current circulation by calculation
	circulation := common.Fixed64(3300 * 10000 * 100000000)
	if proposal.VotersRejectAmount >= common.Fixed64(float64(circulation)*
		p.params.VoterRejectPercentage/100.0) {
		history.Append(height, func() {
			proposal.Status = VoterCanceled
		}, func() {
			proposal.Status = CRAgreed
		})
	} else {
		history.Append(height, func() {
			proposal.Status = VoterAgreed
		}, func() {
			proposal.Status = CRAgreed
		})
	}
}

// shouldEndCRCVote returns if current height should end CRC vote about
// 	the specified proposal.
func (p *ProposalManager) shouldEndCRCVote(hash common.Uint256,
	height uint32) bool {
	proposal := p.getProposal(hash)
	if proposal == nil {
		return false
	}
	return proposal.RegisterHeight+p.params.ProposalCRVotingPeriod <= height
}

// shouldEndPublicVote returns if current height should end public vote
// about the specified proposal.
func (p *ProposalManager) shouldEndPublicVote(hash common.Uint256,
	height uint32) bool {
	proposal := p.getProposal(hash)
	if proposal == nil {
		return false
	}
	return proposal.VoteStartHeight+p.params.ProposalPublicVotingPeriod <=
		height
}

// registerProposal will register proposal state in proposal manager
func (p *ProposalManager) registerProposal(tx *types.Transaction,
	height uint32, history *utils.History) {
	proposal := tx.Payload.(*payload.CRCProposal)

	proposalState := &ProposalState{
		Status:             Registered,
		Proposal:           *proposal,
		TxHash:             tx.Hash(),
		RegisterHeight:     height,
		CRVotes:            map[common.Uint168]payload.VoteResult{},
		VotersRejectAmount: common.Fixed64(0),
	}

	history.Append(height, func() {
		p.Proposals[proposal.Hash()] = proposalState
	}, func() {
		delete(p.Proposals, proposal.Hash())
	})
}

func getDIDByCode(code []byte) (*common.Uint168, error) {
	ct1, error := contract.CreateCRDIDContractByCode(code)
	if error != nil {
		return nil, error
	}
	return ct1.ToProgramHash(), error
}

// registerProposal will register proposal state in proposal manager
func (p *ProposalManager) proposalReview(tx *types.Transaction,
	height uint32, history *utils.History) {
	proposalReview := tx.Payload.(*payload.CRCProposalReview)
	proposalState := p.getProposal(proposalReview.ProposalHash)
	if proposalState == nil {
		return
	}
	did := proposalReview.DID
	history.Append(height, func() {
		proposalState.CRVotes[did] = proposalReview.VoteResult
	}, func() {
		delete(proposalState.CRVotes, did)
	})
}

func NewProposalManager(params *config.Params) *ProposalManager {
	return &ProposalManager{
		params:           params,
		ProposalKeyFrame: *NewProposalKeyFrame(),
	}
}