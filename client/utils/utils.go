package utils

import (
	"fmt"
	"os"

	"github.com/cosmos/cosmos-sdk/client/context"
	"github.com/cosmos/cosmos-sdk/client/keys"
	sdk "github.com/cosmos/cosmos-sdk/types"
	auth "github.com/cosmos/cosmos-sdk/x/auth"
	authtxb "github.com/cosmos/cosmos-sdk/x/auth/client/txbuilder"
	amino "github.com/tendermint/go-amino"
	"github.com/tendermint/tendermint/libs/common"
)

// CompleteAndBroadcastTxCli implements a auxiliary handler that facilitates sending a series of
// messages in a signed transaction given a TxBuilder and a QueryContext. It
// ensures that the account exists, has a proper number and sequence set. In
// addition, it builds and signs a transaction with the supplied messages.
// Finally, it broadcasts the signed transaction to a node.
func CompleteAndBroadcastTxCli(txBldr authtxb.TxBuilder, cliCtx context.CLIContext, msgs []sdk.Msg) error {
	txBldr, err := prepareTxBuilder(txBldr, cliCtx)
	if err != nil {
		return err
	}
	autogas := cliCtx.DryRun || (cliCtx.Gas == 0)
	if autogas {
		txBldr, err = EnrichCtxWithGas(txBldr, cliCtx, cliCtx.FromAddressName, msgs)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "estimated gas = %v\n", txBldr.Gas)
	}
	if cliCtx.DryRun {
		return nil
	}

	passphrase, err := keys.GetPassphrase(cliCtx.FromAddressName)
	if err != nil {
		return err
	}

	// build and sign the transaction
	txBytes, err := txBldr.BuildAndSign(cliCtx.FromAddressName, passphrase, msgs)
	if err != nil {
		return err
	}
	// broadcast to a Tendermint node
	return cliCtx.EnsureBroadcastTx(txBytes)
}

/*
func CompleteAndBroadcastTxREST(txBldr authctx.TxBuilder, cliCtx context.CLIContext, msgs []sdk.Msg) error {

	adjustment, ok := utils.ParseFloat64OrReturnBadRequest(w, m.GasAdjustment, cliclient.DefaultGasAdjustment)
	if !ok {
		return
	}
	cliCtx = cliCtx.WithGasAdjustment(adjustment)

	if utils.HasDryRunArg(r) || m.Gas == 0 {
		newCtx, err := utils.EnrichCtxWithGas(txBldr, cliCtx, m.LocalAccountName, []sdk.Msg{msg})
		if err != nil {
			utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		if utils.HasDryRunArg(r) {
			utils.WriteSimulationResponse(w, txBldr.Gas)
			return
		}
		txBldr = newCtx
	}

	if utils.HasGenerateOnlyArg(r) {
		utils.WriteGenerateStdTxResponse(w, txBldr, []sdk.Msg{msg})
		return
	}

	txBytes, err := txBldr.BuildAndSign(m.LocalAccountName, m.Password, []sdk.Msg{msg})
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusUnauthorized, err.Error())
		return
	}

	res, err := cliCtx.BroadcastTx(txBytes)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	output, err := wire.MarshalJSONIndent(cdc, res)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Write(output)
}
*/

// SimulateMsgs simulates the transaction and returns the gas estimate and the adjusted value.
func SimulateMsgs(txBldr authtxb.TxBuilder, cliCtx context.CLIContext, name string, msgs []sdk.Msg, gas int64) (estimated, adjusted int64, err error) {
	txBytes, err := txBldr.WithGas(gas).BuildWithPubKey(name, msgs)
	if err != nil {
		return
	}
	estimated, adjusted, err = CalculateGas(cliCtx.Query, cliCtx.Codec, txBytes, cliCtx.GasAdjustment)
	return
}

// EnrichCtxWithGas calculates the gas estimate that would be consumed by the
// transaction and set the transaction's respective value accordingly.
func EnrichCtxWithGas(txBldr authtxb.TxBuilder, cliCtx context.CLIContext, name string, msgs []sdk.Msg) (authtxb.TxBuilder, error) {
	_, adjusted, err := SimulateMsgs(txBldr, cliCtx, name, msgs, 0)
	if err != nil {
		return txBldr, err
	}
	return txBldr.WithGas(adjusted), nil
}

// CalculateGas simulates the execution of a transaction and returns
// both the estimate obtained by the query and the adjusted amount.
func CalculateGas(queryFunc func(string, common.HexBytes) ([]byte, error), cdc *amino.Codec, txBytes []byte, adjustment float64) (estimate, adjusted int64, err error) {
	// run a simulation (via /app/simulate query) to
	// estimate gas and update TxBuilder accordingly
	rawRes, err := queryFunc("/app/simulate", txBytes)
	if err != nil {
		return
	}
	estimate, err = parseQueryResponse(cdc, rawRes)
	if err != nil {
		return
	}
	adjusted = adjustGasEstimate(estimate, adjustment)
	return
}

// PrintUnsignedStdTx builds an unsigned StdTx and prints it to os.Stdout.
func PrintUnsignedStdTx(txBldr authtxb.TxBuilder, cliCtx context.CLIContext, msgs []sdk.Msg) (err error) {
	stdTx, err := buildUnsignedStdTx(txBldr, cliCtx, msgs)
	if err != nil {
		return
	}
	json, err := txBldr.Codec.MarshalJSON(stdTx)
	if err == nil {
		fmt.Printf("%s\n", json)
	}
	return
}

func adjustGasEstimate(estimate int64, adjustment float64) int64 {
	return int64(adjustment * float64(estimate))
}

func parseQueryResponse(cdc *amino.Codec, rawRes []byte) (int64, error) {
	var simulationResult sdk.Result
	if err := cdc.UnmarshalBinary(rawRes, &simulationResult); err != nil {
		return 0, err
	}
	return simulationResult.GasUsed, nil
}

func prepareTxBuilder(txBldr authtxb.TxBuilder, cliCtx context.CLIContext) (authtxb.TxBuilder, error) {
	if err := cliCtx.EnsureAccountExists(); err != nil {
		return txBldr, err
	}

	from, err := cliCtx.GetFromAddress()
	if err != nil {
		return txBldr, err
	}

	// TODO: (ref #1903) Allow for user supplied account number without
	// automatically doing a manual lookup.
	if txBldr.AccountNumber == 0 {
		accNum, err := cliCtx.GetAccountNumber(from)
		if err != nil {
			return txBldr, err
		}
		txBldr = txBldr.WithAccountNumber(accNum)
	}

	// TODO: (ref #1903) Allow for user supplied account sequence without
	// automatically doing a manual lookup.
	if txBldr.Sequence == 0 {
		accSeq, err := cliCtx.GetAccountSequence(from)
		if err != nil {
			return txBldr, err
		}
		txBldr = txBldr.WithSequence(accSeq)
	}
	return txBldr, nil
}

// buildUnsignedStdTx builds a StdTx as per the parameters passed in the
// contexts. Gas is automatically estimated if gas wanted is set to 0.
func buildUnsignedStdTx(txBldr authtxb.TxBuilder, cliCtx context.CLIContext, msgs []sdk.Msg) (stdTx auth.StdTx, err error) {
	txBldr, err = prepareTxBuilder(txBldr, cliCtx)
	if err != nil {
		return
	}
	if txBldr.Gas == 0 {
		txBldr, err = EnrichCtxWithGas(txBldr, cliCtx, cliCtx.FromAddressName, msgs)
		if err != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "estimated gas = %v\n", txBldr.Gas)
	}
	stdSignMsg, err := txBldr.Build(msgs)
	if err != nil {
		return
	}
	return auth.NewStdTx(stdSignMsg.Msgs, stdSignMsg.Fee, nil, stdSignMsg.Memo), nil
}
