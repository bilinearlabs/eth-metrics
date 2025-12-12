package metrics

import (
	"context"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/attestantio/go-eth2-client/api"
	"github.com/attestantio/go-eth2-client/http"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/avast/retry-go/v4"
	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type EpochBlockData struct {
	Withdrawals  map[uint64]*big.Int
	ProposerTips map[uint64]*big.Int
}

type BlockData struct {
	consensusClient   *http.Service
	executionClient   *ethclient.Client
	networkParameters *NetworkParameters
	config            *config.Config
}

func NewBlockData(
	consensusClient *http.Service,
	executionClient *ethclient.Client,
	networkParameters *NetworkParameters,
	config *config.Config,
) (*BlockData, error) {
	return &BlockData{
		consensusClient:   consensusClient,
		executionClient:   executionClient,
		networkParameters: networkParameters,
		config:            config,
	}, nil
}

func (b *BlockData) GetEpochBlockData(epoch uint64) (*EpochBlockData, error) {
	log.Info("Fetching block data for epoch: ", epoch)

	data := &EpochBlockData{
		Withdrawals:  make(map[uint64]*big.Int),
		ProposerTips: make(map[uint64]*big.Int),
	}

	firstSlot := epoch * b.networkParameters.slotsInEpoch
	for slot := firstSlot; slot < firstSlot+b.networkParameters.slotsInEpoch; slot++ {
		slotStr := strconv.FormatUint(slot, 10)
		opts := api.SignedBeaconBlockOpts{
			Block: slotStr,
		}

		beaconBlock, err := b.consensusClient.SignedBeaconBlock(
			context.Background(),
			&opts,
		)
		if err != nil {
			// This error is expected in skipped or orphaned blocks
			if !strings.Contains(err.Error(), "NOT_FOUND") {
				return nil, errors.Wrap(err, "error getting signed beacon block")
			}
			log.Warn("block not found for slot: ", slot)
			continue
		}

		block := beaconBlock.Data

		b.extractWithdrawals(block, data.Withdrawals)

		// Extract transaction fees
		proposerTip, err := b.GetProposerTip(block)
		if err != nil {
			return nil, errors.Wrap(err, "error getting proposer tip")
		}
		proposerIndex := b.GetProposerIndex(block)
		if _, ok := data.ProposerTips[proposerIndex]; !ok {
			data.ProposerTips[proposerIndex] = big.NewInt(0)
		}
		data.ProposerTips[proposerIndex].Add(data.ProposerTips[proposerIndex], proposerTip)
	}

	return data, nil
}

func (b *BlockData) extractWithdrawals(beaconBlock *spec.VersionedSignedBeaconBlock, withdrawals map[uint64]*big.Int) {
	blockWithdrawals := b.GetBlockWithdrawals(beaconBlock)
	for _, withdrawal := range blockWithdrawals {
		idx := uint64(withdrawal.ValidatorIndex)
		if _, ok := withdrawals[idx]; !ok {
			withdrawals[idx] = big.NewInt(0)
		}
		withdrawals[idx].Add(withdrawals[idx], big.NewInt(int64(withdrawal.Amount)))
	}
}

func (b *BlockData) GetProposerTip(beaconBlock *spec.VersionedSignedBeaconBlock) (*big.Int, error) {
	blockNumber := b.GetBlockNumber(beaconBlock)
	rawTxs := b.GetBlockTransactions(beaconBlock)
	retryOpts := []retry.Option{
		retry.Attempts(5),
		retry.Delay(5 * time.Second),
	}
	header, err := b.getBlockHeader(blockNumber, retryOpts)
	if err != nil {
		return nil, errors.Wrap(err, "error getting block header and receipts")
	}
	baseFeePerGasBytes := b.GetBaseFeePerGas(beaconBlock)
	baseFeePerGas := new(big.Int).SetBytes(baseFeePerGasBytes[:])

	tips := big.NewInt(0)
	for _, rawTx := range rawTxs {
		var tx types.Transaction
		err = tx.UnmarshalBinary(rawTx)
		if err != nil {
			return nil, errors.Wrap(err, "error unmarshalling transaction")
		}
		txReceipt, err := b.getTransactionReceipt(&tx, retryOpts)
		if err != nil {
			return nil, errors.Wrap(err, "error getting block receipt")
		}
		if err != nil {
			return nil, errors.Wrap(err, "error unmarshalling transaction")
		}
		if tx.Hash() != txReceipt.TxHash {
			return nil, errors.New("transaction hash mismatch")
		}

		tipFee := new(big.Int)
		gasPrice := tx.GasPrice()
		gasUsed := big.NewInt(int64(txReceipt.GasUsed))

		switch tx.Type() {
		case 0, 1:
			tipFee.Mul(gasPrice, gasUsed)
		case 2, 3, 4:
			tip := new(big.Int).Add(tx.GasTipCap(), header.BaseFee)
			gasFeeCap := tx.GasFeeCap()
			var usedGasPrice *big.Int
			if gasFeeCap.Cmp(tip) > 0 {
				usedGasPrice = gasFeeCap
			} else {
				usedGasPrice = tip
			}
			tipFee = new(big.Int).Mul(usedGasPrice, gasUsed)
		default:
			return nil, errors.Errorf("unknown transaction type: %d, hash: %s", tx.Type(), tx.Hash().String())
		}
		tips.Add(tips, tipFee)
	}
	burnt := new(big.Int).Mul(big.NewInt(int64(b.GetGasUsed(beaconBlock))), baseFeePerGas)
	proposerReward := new(big.Int).Sub(tips, burnt)
	return proposerReward, nil
}

func (b *BlockData) getBlockHeader(
	blockNumber uint64,
	retryOpts []retry.Option,
) (*types.Header, error) {
	var header *types.Header
	var err error

	blockNumberBig := new(big.Int).SetUint64(blockNumber)

	err = retry.Do(func() error {
		header, err = b.executionClient.HeaderByNumber(context.Background(), blockNumberBig)
		if err != nil {
			log.Warnf("error getting header for block %d: %s. Retrying...", blockNumber, err)
			return errors.Wrap(err, "error getting header for block")
		}
		return nil
	}, retryOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "error getting header for block "+blockNumberBig.String())
	}

	return header, nil
}

func (b *BlockData) getTransactionReceipt(tx *types.Transaction, retryOpts []retry.Option) (*types.Receipt, error) {
	var receipt *types.Receipt
	var err error
	err = retry.Do(func() error {
		receipt, err = b.executionClient.TransactionReceipt(context.Background(), tx.Hash())
		if err != nil {
			log.Warnf("error getting transaction receipt for tx %s: %s. Retrying...", tx.Hash().String(), err)
			return errors.Wrap(err, "error getting transaction receipt")
		}
		return nil
	}, retryOpts...)

	if err != nil {
		return nil, errors.Wrap(err, "error getting transaction receipt for tx "+tx.Hash().String())
	}

	return receipt, nil
}

func (b *BlockData) GetBlockWithdrawals(beaconBlock *spec.VersionedSignedBeaconBlock) []*capella.Withdrawal {
	var withdrawals []*capella.Withdrawal
	if beaconBlock.Altair != nil {
		withdrawals = []*capella.Withdrawal{}
	} else if beaconBlock.Bellatrix != nil {
		withdrawals = []*capella.Withdrawal{}
	} else if beaconBlock.Capella != nil {
		withdrawals = beaconBlock.Capella.Message.Body.ExecutionPayload.Withdrawals
	} else if beaconBlock.Deneb != nil {
		withdrawals = beaconBlock.Deneb.Message.Body.ExecutionPayload.Withdrawals
	} else if beaconBlock.Electra != nil {
		withdrawals = beaconBlock.Electra.Message.Body.ExecutionPayload.Withdrawals
	} else if beaconBlock.Fulu != nil {
		withdrawals = beaconBlock.Fulu.Message.Body.ExecutionPayload.Withdrawals
	} else {
		log.Fatal("Beacon block was empty")
	}
	return withdrawals
}

func (b *BlockData) GetBlockTransactions(beaconBlock *spec.VersionedSignedBeaconBlock) []bellatrix.Transaction {
	var transactions []bellatrix.Transaction
	if beaconBlock.Altair != nil {
		transactions = []bellatrix.Transaction{}
	} else if beaconBlock.Bellatrix != nil {
		transactions = beaconBlock.Bellatrix.Message.Body.ExecutionPayload.Transactions
	} else if beaconBlock.Capella != nil {
		transactions = beaconBlock.Capella.Message.Body.ExecutionPayload.Transactions
	} else if beaconBlock.Deneb != nil {
		transactions = beaconBlock.Deneb.Message.Body.ExecutionPayload.Transactions
	} else if beaconBlock.Electra != nil {
		transactions = beaconBlock.Electra.Message.Body.ExecutionPayload.Transactions
	} else if beaconBlock.Fulu != nil {
		transactions = beaconBlock.Fulu.Message.Body.ExecutionPayload.Transactions
	} else {
		log.Fatal("Beacon block was empty")
	}
	return transactions
}

func (b *BlockData) GetBlockNumber(beaconBlock *spec.VersionedSignedBeaconBlock) uint64 {
	var blockNumber uint64
	if beaconBlock.Altair != nil {
		log.Fatal("Altair block has no block number")
	} else if beaconBlock.Bellatrix != nil {
		blockNumber = beaconBlock.Bellatrix.Message.Body.ExecutionPayload.BlockNumber
	} else if beaconBlock.Capella != nil {
		blockNumber = beaconBlock.Capella.Message.Body.ExecutionPayload.BlockNumber
	} else if beaconBlock.Deneb != nil {
		blockNumber = beaconBlock.Deneb.Message.Body.ExecutionPayload.BlockNumber
	} else if beaconBlock.Electra != nil {
		blockNumber = beaconBlock.Electra.Message.Body.ExecutionPayload.BlockNumber
	} else if beaconBlock.Fulu != nil {
		blockNumber = beaconBlock.Fulu.Message.Body.ExecutionPayload.BlockNumber
	} else {
		log.Fatal("Beacon block was empty")
	}
	return blockNumber
}

// Returns base fee per gas in big endian
func (b *BlockData) GetBaseFeePerGas(beaconBlock *spec.VersionedSignedBeaconBlock) [32]byte {
	var baseFeePerGas [32]byte

	if beaconBlock.Altair != nil {
		log.Fatal("Altair block has no base fee per gas")
	} else if beaconBlock.Bellatrix != nil {
		baseFeePerGasLE := beaconBlock.Bellatrix.Message.Body.ExecutionPayload.BaseFeePerGas
		for i := range 32 {
			baseFeePerGas[i] = baseFeePerGasLE[32-1-i]
		}
	} else if beaconBlock.Capella != nil {
		baseFeePerGasLE := beaconBlock.Capella.Message.Body.ExecutionPayload.BaseFeePerGas
		for i := range 32 {
			baseFeePerGas[i] = baseFeePerGasLE[32-1-i]
		}
	} else if beaconBlock.Deneb != nil {
		baseFeePerGas = beaconBlock.Deneb.Message.Body.ExecutionPayload.BaseFeePerGas.Bytes32()
	} else if beaconBlock.Electra != nil {
		baseFeePerGas = beaconBlock.Electra.Message.Body.ExecutionPayload.BaseFeePerGas.Bytes32()
	} else if beaconBlock.Fulu != nil {
		baseFeePerGas = beaconBlock.Fulu.Message.Body.ExecutionPayload.BaseFeePerGas.Bytes32()
	} else {
		log.Fatal("Beacon block was empty")
	}
	return baseFeePerGas
}

func (b *BlockData) GetGasUsed(beaconBlock *spec.VersionedSignedBeaconBlock) uint64 {
	var gasUsed uint64

	if beaconBlock.Altair != nil {
		log.Fatal("Altair block has no gas used")
	} else if beaconBlock.Bellatrix != nil {
		gasUsed = beaconBlock.Bellatrix.Message.Body.ExecutionPayload.GasUsed
	} else if beaconBlock.Capella != nil {
		gasUsed = beaconBlock.Capella.Message.Body.ExecutionPayload.GasUsed
	} else if beaconBlock.Deneb != nil {
		gasUsed = beaconBlock.Deneb.Message.Body.ExecutionPayload.GasUsed
	} else if beaconBlock.Electra != nil {
		gasUsed = beaconBlock.Electra.Message.Body.ExecutionPayload.GasUsed
	} else if beaconBlock.Fulu != nil {
		gasUsed = beaconBlock.Fulu.Message.Body.ExecutionPayload.GasUsed
	} else {
		log.Fatal("Beacon block was empty")
	}
	return gasUsed
}

func (b *BlockData) GetProposerIndex(beaconBlock *spec.VersionedSignedBeaconBlock) uint64 {
	var proposerIndex uint64
	if beaconBlock.Altair != nil {
		proposerIndex = uint64(beaconBlock.Altair.Message.ProposerIndex)
	} else if beaconBlock.Bellatrix != nil {
		proposerIndex = uint64(beaconBlock.Bellatrix.Message.ProposerIndex)
	} else if beaconBlock.Capella != nil {
		proposerIndex = uint64(beaconBlock.Capella.Message.ProposerIndex)
	} else if beaconBlock.Deneb != nil {
		proposerIndex = uint64(beaconBlock.Deneb.Message.ProposerIndex)
	} else if beaconBlock.Electra != nil {
		proposerIndex = uint64(beaconBlock.Electra.Message.ProposerIndex)
	} else if beaconBlock.Fulu != nil {
		proposerIndex = uint64(beaconBlock.Fulu.Message.ProposerIndex)
	} else {
		log.Fatal("Beacon block was empty")
	}
	return proposerIndex
}
