package metrics

import (
	"context"
	"encoding/base64"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/attestantio/go-eth2-client/api"
	"github.com/attestantio/go-eth2-client/http"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/rs/zerolog"

	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/bilinearlabs/eth-metrics/db"
	"github.com/bilinearlabs/eth-metrics/pools"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type NetworkParameters struct {
	genesisSeconds uint64
	slotsInEpoch   uint64
	secondsPerSlot uint64
}

type Metrics struct {
	networkParameters    *NetworkParameters
	config               *config.Config
	db                   *db.Database
	httpClient           *http.Service
	validatorKeysPerPool map[string][][]byte
	validatorKeyToPool   map[string]string
	beaconState          *BeaconState
	proposalDuties       *ProposalDuties
	relayRewards         *RelayRewards
}

func NewMetrics(
	ctx context.Context,
	config *config.Config) (*Metrics, error) {

	var database *db.Database
	var err error

	if config.DatabasePath != "" {
		database, err = db.New(config.DatabasePath)
		if err != nil {
			return nil, errors.Wrap(err, "could not create postgresql")
		}
		err = database.CreateTables()
		if err != nil {
			return nil, errors.Wrap(err, "error creating pool table to store data")
		}
	}

	var validatorKeysPerPool map[string][][]byte
	var validatorKeyToPool map[string]string

	if config.ValidatorsFile != "" {
		validatorKeysPerPool, validatorKeyToPool, err = pools.ReadValidatorsFile(config.ValidatorsFile)
		if err != nil {
			return nil, errors.Wrap(err, "error reading validators file")
		}
	} else {
		// TODO check if mantain reading from txt files
		validatorKeysPerPool = make(map[string][][]byte)
		validatorKeyToPool = make(map[string]string)
		for _, poolName := range config.PoolNames {
			if strings.HasSuffix(poolName, ".txt") {
				pubKeysDeposited, err := pools.ReadCustomValidatorsFile(poolName)
				if err != nil {
					log.Fatal(err)
				}
				validatorKeysPerPool[poolName] = pubKeysDeposited
				for _, key := range pubKeysDeposited {
					keyStr := hexutil.Encode(key)
					validatorKeyToPool[keyStr] = poolName
				}
				log.Info("File: ", poolName, " contains ", len(pubKeysDeposited), " keys")
			}
		}
	}

	// Add header with credentials if provided
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(config.Credentials))
	cred := map[string]string{}
	if config.Credentials != "" {
		cred["Authorization"] = "Basic " + encodedCredentials
	}

	client, err := http.New(context.Background(),
		http.WithTimeout(60*time.Second),
		http.WithAddress(config.Eth2Address),
		http.WithLogLevel(zerolog.WarnLevel),
		http.WithExtraHeaders(cred),
	)
	if err != nil {
		return nil, err
	}

	httpClient := client.(*http.Service)

	genesis, err := httpClient.Genesis(context.Background(), &api.GenesisOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "error getting genesis info")
	}

	spec, err := httpClient.Spec(context.Background(), &api.SpecOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "error getting spec info")
	}

	slotsPerEpochInterface, found := spec.Data["SLOTS_PER_EPOCH"]
	if !found {
		return nil, errors.New("SLOTS_PER_EPOCH not found in spec")
	}

	secondsPerSlotInterface, found := spec.Data["SECONDS_PER_SLOT"]
	if !found {
		return nil, errors.New("SECONDS_PER_SLOT not found in spec")
	}

	slotsPerEpoch := slotsPerEpochInterface.(uint64)

	secondsPerSlot := uint64(secondsPerSlotInterface.(time.Duration).Seconds())

	log.Info("Genesis time: ", genesis.Data.GenesisTime.Unix())
	log.Info("Slots per epoch: ", slotsPerEpoch)
	log.Info("Seconds per slot: ", secondsPerSlot)

	networkParameters := &NetworkParameters{
		genesisSeconds: uint64(genesis.Data.GenesisTime.Unix()),
		slotsInEpoch:   slotsPerEpoch,
		secondsPerSlot: secondsPerSlot,
	}

	return &Metrics{
		networkParameters:    networkParameters,
		db:                   database,
		httpClient:           httpClient,
		config:               config,
		validatorKeysPerPool: validatorKeysPerPool,
		validatorKeyToPool:   validatorKeyToPool,
	}, nil
}

func (a *Metrics) Run() {
	bc, err := NewBeaconState(
		a.httpClient,
		a.networkParameters,
		a.db,
		a.config,
		a.networkParameters.slotsInEpoch,
	)
	if err != nil {
		log.Fatal(err)
		// TODO: Add return here.
	}
	a.beaconState = bc

	pd, err := NewProposalDuties(
		a.httpClient,
		a.networkParameters,
		a.db,
		a.config,
	)

	if err != nil {
		log.Fatal(err)
	}
	a.proposalDuties = pd

	rr, err := NewRelayRewards(a.networkParameters, a.validatorKeyToPool, a.config)
	if err != nil {
		log.Fatal(err)
	}
	a.relayRewards = rr

	for _, poolName := range a.config.PoolNames {
		// Check that the validator keys are correct
		_, _, err := a.GetValidatorKeys(poolName)
		if err != nil {
			log.Fatal(err)
		}

	}
	go a.Loop()
}

func (a *Metrics) Loop() {
	var prevEpoch uint64 = uint64(0)
	var prevBeaconState *spec.VersionedBeaconState = nil
	// TODO: Refactor and hoist some stuff out to a function
	for {
		// Before doing anything, check if we are in the next epoch
		opts := api.NodeSyncingOpts{
			Common: api.CommonOpts{
				Timeout: 5 * time.Second,
			},
		}
		headSlot, err := a.httpClient.NodeSyncing(context.Background(), &opts)
		if err != nil {
			log.Error("Could not get node sync status:", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if headSlot.Data.IsSyncing {
			log.Error("Node is not in sync")
			time.Sleep(5 * time.Second)
			continue
		}

		// Leave some maring of 2 epochs
		currentEpoch := uint64(headSlot.Data.HeadSlot)/uint64(a.networkParameters.slotsInEpoch) - 2

		// If a debug epoch is set, overwrite the slot. Will compute just metrics for that epoch
		if a.config.EpochDebug != "" {
			epochDebugUint64, err := strconv.ParseUint(a.config.EpochDebug, 10, 64)
			if err != nil {
				log.Fatal(err)
			}
			log.Warn("Debugging mode, calculating metrics for epoch: ", a.config.EpochDebug)
			currentEpoch = epochDebugUint64
		}

		if prevEpoch >= currentEpoch {
			// do nothing
			time.Sleep(5 * time.Second)
			continue
		}

		missingEpochs, err := a.db.GetMissingEpochs(currentEpoch, a.config.BackfillEpochs)
		if err != nil {
			log.Error(err)
			time.Sleep(5 * time.Second)
			continue
		}

		if len(missingEpochs) > 0 {
			log.Info("Backfilling epochs: ", missingEpochs)
		}

		// Do backfilling.
		for _, epoch := range missingEpochs {
			if prevBeaconState != nil {
				prevSlot, err := prevBeaconState.Slot()
				prevEpoch = uint64(prevSlot) % a.networkParameters.slotsInEpoch
				if err != nil {
					// TODO: Handle this gracefully
					log.Fatal(err, "error getting slot from previous beacon state")
				}
				if (prevEpoch + 1) != epoch {
					prevBeaconState = nil
				}
			}
			currentBeaconState, err := a.ProcessEpoch(epoch, prevBeaconState)
			if err != nil {
				log.Error(err)
				time.Sleep(5 * time.Second)
				continue
			}
			prevBeaconState = currentBeaconState
		}

		currentBeaconState, err := a.ProcessEpoch(currentEpoch, prevBeaconState)
		if err != nil {
			log.Error(err)
			time.Sleep(5 * time.Second)
			continue
		}

		prevBeaconState = currentBeaconState
		prevEpoch = currentEpoch

		if a.config.EpochDebug != "" {
			log.Warn("Running in debug mode, exiting ok.")
			os.Exit(0)
		}
	}
}

func (a *Metrics) ProcessEpoch(
	currentEpoch uint64,
	prevBeaconState *spec.VersionedBeaconState) (*spec.VersionedBeaconState, error) {
	// Fetch proposal duties, meaning who shall propose each block within this epoch
	duties, err := a.proposalDuties.GetProposalDuties(currentEpoch)
	if err != nil {
		return nil, errors.Wrap(err, "error getting proposal duties")
	}

	// Fetch who actually proposed the blocks in this epoch
	proposed, err := a.proposalDuties.GetProposedBlocks(currentEpoch)
	if err != nil {
		return nil, errors.Wrap(err, "error getting proposed blocks")
	}

	// Summarize duties + proposed in a struct
	proposalMetrics, err := a.proposalDuties.GetProposalMetrics(duties, proposed)
	if err != nil {
		return nil, errors.Wrap(err, "error getting proposal metrics")
	}

	currentBeaconState, err := a.beaconState.GetBeaconState(currentEpoch)
	if err != nil {
		return nil, errors.Wrap(err, "error fetching beacon state")
	}

	// if no prev beacon state is known, fetch it
	if prevBeaconState == nil {
		prevBeaconState, err = a.beaconState.GetBeaconState(currentEpoch - 1)
		if err != nil {
			return nil, errors.Wrap(err, "error fetching previous beacon state")
		}
	}

	// Map to quickly convert public keys to index
	valKeyToIndex := PopulateKeysToIndexesMap(currentBeaconState)

	relayRewardsPerPool, err := a.relayRewards.GetRelayRewards(currentEpoch)
	if err != nil {
		return nil, errors.Wrap(err, "error getting relay rewards")
	}

	// Get withdrawals from all blocks of the epoch
	validatorIndexToWithdrawalAmount, err := a.GetEpochWithdrawals(currentEpoch)
	if err != nil {
		return nil, errors.Wrap(err, "error getting epoch withdrawals")
	}
	// Iterate all pools and calculate metrics using the fetched data
	for poolName, pubKeys := range a.validatorKeysPerPool {
		validatorIndexes := GetIndexesFromKeys(pubKeys, valKeyToIndex)

		relayRewards := big.NewInt(0)
		if reward, ok := relayRewardsPerPool[poolName]; ok {
			relayRewards.Add(relayRewards, reward)
		}
		err = a.beaconState.Run(pubKeys, poolName, currentBeaconState, prevBeaconState, valKeyToIndex, relayRewards, validatorIndexToWithdrawalAmount)
		if err != nil {
			return nil, errors.Wrap(err, "error running beacon state")
		}

		err = a.proposalDuties.RunProposalMetrics(validatorIndexes, poolName, &proposalMetrics)
		if err != nil {
			return nil, errors.Wrap(err, "error running proposal metrics")
		}
	}

	return currentBeaconState, nil
}

func (a *Metrics) GetValidatorKeys(poolName string) (string, [][]byte, error) {
	var pubKeysDeposited [][]byte
	var err error
	if strings.HasSuffix(poolName, ".txt") {
		// Vanila file, one key per line
		pubKeysDeposited, err = pools.ReadCustomValidatorsFile(poolName)
		if err != nil {
			log.Fatal(err)
		}
		// trim the file path and extension
		poolName = filepath.Base(poolName)
		poolName = strings.TrimSuffix(poolName, filepath.Ext(poolName))
	} else if strings.HasSuffix(poolName, ".csv") {
		// ethsta.com format
		pubKeysDeposited, err = pools.ReadEthstaValidatorsFile(poolName)
		if err != nil {
			log.Fatal(err)
		}
		// trim the file path and extension
		poolName = filepath.Base(poolName)
		poolName = strings.TrimSuffix(poolName, filepath.Ext(poolName))

	}
	return poolName, pubKeysDeposited, nil
}

func (a *Metrics) GetEpochWithdrawals(epoch uint64) (map[uint64]*big.Int, error) {
	validatorIndexToWithdrawalAmount := make(map[uint64]*big.Int)
	firstSlot := epoch * a.networkParameters.slotsInEpoch
	for slot := firstSlot; slot < firstSlot+a.networkParameters.slotsInEpoch; slot++ {
		slotStr := strconv.FormatUint(slot, 10)
		opts := api.SignedBeaconBlockOpts{
			Block: slotStr,
		}

		beaconBlock, err := a.httpClient.SignedBeaconBlock(
			context.Background(),
			&opts,
		)
		if err != nil {
			log.Warn("block not found for slot: ", slot)
			continue
		}
		withdrawals := GetBlockWithdrawals(beaconBlock.Data)

		for _, withdrawal := range withdrawals {
			if _, ok := validatorIndexToWithdrawalAmount[uint64(withdrawal.ValidatorIndex)]; !ok {
				validatorIndexToWithdrawalAmount[uint64(withdrawal.ValidatorIndex)] = big.NewInt(0)
			}
			validatorIndexToWithdrawalAmount[uint64(withdrawal.ValidatorIndex)].Add(validatorIndexToWithdrawalAmount[uint64(withdrawal.ValidatorIndex)], big.NewInt(int64(withdrawal.Amount)))
		}
	}
	return validatorIndexToWithdrawalAmount, nil
}

func GetBlockWithdrawals(beaconBlock *spec.VersionedSignedBeaconBlock) []*capella.Withdrawal {
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
	} else {
		log.Fatal("Beacon state was empty")
	}
	return withdrawals
}
