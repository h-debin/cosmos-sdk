package gov

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/tags"
)

// Called every block, process inflation, update validator set
func EndBlocker(ctx sdk.Context, keeper Keeper) sdk.Tags {
	logger := ctx.Logger().With("module", "x/gov")
	resTags := sdk.NewTags()

	inactiveIterator := keeper.InactiveProposalQueueIterator(ctx, ctx.BlockHeader().Time)
	defer inactiveIterator.Close()
	for ; inactiveIterator.Valid(); inactiveIterator.Next() {
		var proposalID uint64

		keeper.cdc.MustUnmarshalBinaryLengthPrefixed(inactiveIterator.Value(), &proposalID)
		inactiveProposal, ok := keeper.GetProposal(ctx, proposalID)
		if !ok {
			panic(fmt.Sprintf("proposal %d does not exist", proposalID))
		}

		keeper.DeleteProposal(ctx, proposalID)
		keeper.DeleteDeposits(ctx, proposalID) // delete any associated deposits (burned)

		resTags = resTags.AppendTag(tags.ProposalID, fmt.Sprintf("%d", proposalID))
		resTags = resTags.AppendTag(tags.ProposalResult, tags.ActionProposalDropped)

		logger.Info(
			fmt.Sprintf("proposal %d (%s) didn't meet minimum deposit of %s (had only %s); deleted",
				inactiveProposal.ProposalID,
				inactiveProposal.GetTitle(),
				keeper.GetDepositParams(ctx).MinDeposit,
				inactiveProposal.TotalDeposit,
			),
		)
	}

	// fetch active proposals whose voting periods have ended (are passed the block time)
	activeIterator := keeper.ActiveProposalQueueIterator(ctx, ctx.BlockHeader().Time)
	defer activeIterator.Close()
	for ; activeIterator.Valid(); activeIterator.Next() {
		var proposalID uint64

		keeper.cdc.MustUnmarshalBinaryLengthPrefixed(activeIterator.Value(), &proposalID)
		activeProposal, ok := keeper.GetProposal(ctx, proposalID)
		if !ok {
			panic(fmt.Sprintf("proposal %d does not exist", proposalID))
		}
		passes, tallyResults := tally(ctx, keeper, activeProposal)

		var tagValue string
		var tagError sdk.Error
		if passes {
			keeper.RefundDeposits(ctx, activeProposal.ProposalID)
			activeProposal.Status = StatusPassed
			tagValue = tags.ActionProposalPassed

			// XXX: should we return error here if the router returns nil?
			// I think we should, app can exclude certain handlers
			// to disable some of the proposal types
			// currently panics(same behaviour with baseapp.router)
			handler := keeper.router.Route(activeProposal.ProposalRoute())
			if handler == nil {
				// SubmitProposal checks whether there is a handler for this proposal already
				// Panic here because it does not make sense that there is no handler exists
				panic(fmt.Sprintf("handler for proposal %d does not exist", proposalID))
			}
			tagError = handler(ctx, activeProposal.Content)
			if tagError == nil {
				tagValue = tags.ActionProposalPassed
			} else {
				tagValue = tags.ActionProposalFailed
			}
		} else {
			keeper.DeleteDeposits(ctx, activeProposal.ProposalID)
			activeProposal.Status = StatusRejected
			tagValue = tags.ActionProposalRejected
		}

		activeProposal.FinalTallyResult = tallyResults
		keeper.SetProposal(ctx, activeProposal)
		keeper.RemoveFromActiveProposalQueue(ctx, activeProposal.VotingEndTime, activeProposal.ProposalID)

		logger.Info(
			fmt.Sprintf(
				"proposal %d (%s) tallied; passed: %v; handled: %v",
				activeProposal.ProposalID, activeProposal.GetTitle(), passes,
				tagValue == tags.ActionProposalPassed,
			),
		)

		resTags = resTags.AppendTag(tags.ProposalID, fmt.Sprintf("%d", proposalID))
		resTags = resTags.AppendTag(tags.ProposalResult, tagValue)
		if tagError != nil {
			resTags = resTags.AppendTag(tags.ProposalError, tagError.Error())
		}
	}

	return resTags
}
