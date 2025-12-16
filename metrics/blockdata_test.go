package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func Test_GetProposerTip(t *testing.T) {
	bd := &BlockData{
		networkParameters: &NetworkParameters{
			slotsInEpoch: 32,
		},
	}

	blockData, err := LoadBlockData(5214302)
	if err != nil {
		t.Fatalf("error loading block data: %s", err)
	}

	proposerTip, err := bd.GetProposerTip(blockData.BeaconBlock, blockData.Header, blockData.Receipts)
	if err != nil {
		t.Fatalf("error getting proposer tip: %s", err)
	}
	tip := big.NewInt(38657065851824731)
	assert.Equal(t, proposerTip, tip)
}

func Test_ExtractWithdrawals(t *testing.T) {
	bd := &BlockData{
		networkParameters: &NetworkParameters{
			slotsInEpoch: 32,
		},
	}

	blockData, err := LoadBlockData(5214302)
	if err != nil {
		t.Fatalf("error loading block data: %s", err)
	}

	withdrawals := make(map[uint64]*big.Int)
	bd.ExtractWithdrawals(blockData.BeaconBlock, withdrawals)
	assert.Equal(t, withdrawals, map[uint64]*big.Int{
		416729: big.NewInt(1701196),
		416730: big.NewInt(1731482),
		416731: big.NewInt(1683530),
		416732: big.NewInt(1765666),
		416733: big.NewInt(1753893),
		416734: big.NewInt(45764133),
		416735: big.NewInt(1740038),
		416736: big.NewInt(1736192),
		416737: big.NewInt(1732742),
		416738: big.NewInt(1776043),
		416739: big.NewInt(1746233),
		416740: big.NewInt(1713045),
		416741: big.NewInt(1761575),
		416742: big.NewInt(1719014),
		416743: big.NewInt(1735415),
		416744: big.NewInt(1714423),
	})
}

type MockBlockData struct {
	BeaconBlock *spec.VersionedSignedBeaconBlock `json:"consensus_block"`
	Header      *types.Header                    `json:"execution_header"`
	Receipts    []*types.Receipt                 `json:"execution_receipts"`
}

func LoadBlockData(slot uint64) (*MockBlockData, error) {
	path := filepath.Join("../mock", fmt.Sprintf("fullblock_slot_%d.json", slot))
	jsonFile, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "could not open json file")
	}
	defer jsonFile.Close()

	byteValue, err := io.ReadAll(jsonFile)
	if err != nil {
		return nil, errors.Wrap(err, "could not read json file")
	}

	var blockData MockBlockData

	err = json.Unmarshal(byteValue, &blockData)
	if err != nil {
		return nil, errors.Wrap(err, "could not unmarshal json file")
	}

	return &blockData, nil
}
