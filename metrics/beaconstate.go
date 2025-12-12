package metrics

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/attestantio/go-eth2-client/api"
	"github.com/attestantio/go-eth2-client/http"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/altair"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/pkg/errors"

	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/bilinearlabs/eth-metrics/db"
	"github.com/bilinearlabs/eth-metrics/schemas"

	log "github.com/sirupsen/logrus"
)

type BeaconState struct {
	consensus         *http.Service
	networkParameters *NetworkParameters
	database          *db.Database
	config            *config.Config
	slotsInEpoch      uint64
}

func NewBeaconState(
	httpClient *http.Service,
	networkParameters *NetworkParameters,
	database *db.Database,
	config *config.Config,
	slotsInEpoch uint64,
) (*BeaconState, error) {

	return &BeaconState{
		consensus:         httpClient,
		networkParameters: networkParameters,
		database:          database,
		config:            config,
		slotsInEpoch:      slotsInEpoch,
	}, nil
}

func (p *BeaconState) Run(
	validatorKeys [][]byte,
	poolName string,
	currentBeaconState *spec.VersionedBeaconState,
	prevBeaconState *spec.VersionedBeaconState,
	valKeyToIndex map[string]uint64,
	relayRewards *big.Int,
	validatorIndexToWithdrawalAmount map[uint64]*big.Int,
	proposerTips map[uint64]*big.Int) error {

	if currentBeaconState == nil || prevBeaconState == nil {
		return errors.New("current or previous beacon state is nil")
	}
	if len(validatorKeys) == 0 {
		return errors.New("no validator keys provided")
	}

	currentSlot, err := currentBeaconState.Slot()
	if err != nil {
		return errors.Wrap(err, "error getting slot from current beacon state")
	}

	prevSlot, err := prevBeaconState.Slot()
	if err != nil {
		return errors.Wrap(err, "error getting slot from previous beacon state")
	}

	// Distance shall be the slots in an epoch
	if currentSlot != (prevSlot + phase0.Slot(p.slotsInEpoch)) {
		return errors.New(fmt.Sprintf("slot mismatch between current and previous beacon state: %d vs %d",
			currentSlot, prevSlot))
	}

	validatorIndexes := GetIndexesFromKeys(validatorKeys, valKeyToIndex)
	activeValidatorIndexes := p.GetActiveIndexes(validatorIndexes, currentBeaconState)

	// TODO: Redundant parameters already in the class
	metrics, err := p.PopulateParticipationAndBalance(
		poolName,
		activeValidatorIndexes,
		currentBeaconState,
		prevBeaconState,
		validatorIndexToWithdrawalAmount)

	if err != nil {
		return errors.Wrap(err, "error populating participation and balance")
	}

	metrics.NOfActiveValidators = uint64(len(activeValidatorIndexes))
	metrics.MEVRewards = relayRewards

	aggregatedProposerTips := big.NewInt(0)
	for _, activeValidatorIndex := range activeValidatorIndexes {
		aggregatedProposerTips.Add(aggregatedProposerTips, proposerTips[activeValidatorIndex])
	}
	metrics.ProposerTips = aggregatedProposerTips

	syncCommitteeKeys := BLSPubKeyToByte(GetCurrentSyncCommittee(currentBeaconState))
	syncCommitteeIndexes := GetIndexesFromKeys(syncCommitteeKeys, valKeyToIndex)
	poolSyncIndexes := GetValidatorsIn(syncCommitteeIndexes, activeValidatorIndexes)

	// Temporal to debug:
	p.ParticipationDebug(activeValidatorIndexes, currentBeaconState)

	log.Info("The pool:", poolName, " contains ", len(validatorKeys), " keys (may be hardcoded)")
	log.Info("The pool:", poolName, " contains ", len(validatorIndexes), " validators detected in the beacon state")
	log.Info("The pool:", poolName, " contains ", len(activeValidatorIndexes), " active validators detected in the beacon state")
	log.Info("Pool: ", poolName, " sync committee validators ", poolSyncIndexes)

	logMetrics(metrics, poolName)

	if p.database != nil {
		err := p.database.StoreValidatorPerformance(metrics)
		if err != nil {
			return errors.Wrap(err, "could not store validator performance")
		}
	}

	return nil
}

// TODO: Very naive approach
func GetValidatorsIn(allSyncCommitteeIndexes []uint64, poolValidatorIndexes []uint64) []uint64 {
	poolCommmitteeIndexes := make([]uint64, 0)
	for i := range allSyncCommitteeIndexes {
		for j := range poolValidatorIndexes {
			if allSyncCommitteeIndexes[i] == poolValidatorIndexes[j] {
				poolCommmitteeIndexes = append(poolCommmitteeIndexes, allSyncCommitteeIndexes[i])
				break
			}
		}
	}
	return poolCommmitteeIndexes
}

// Check if element is in set
// TODO: Move to utils
func IsValidatorIn(element uint64, set []uint64) bool {
	for i := range set {
		if element == set[i] {
			return true
		}
	}
	return false
}

func PopulateKeysToIndexesMap(beaconState *spec.VersionedBeaconState) map[string]uint64 {
	// TODO: Naive approach. Reset the map every time
	valKeyToIndex := make(map[string]uint64, 0)
	for index, beaconStateKey := range GetValidators(beaconState) {
		valKeyToIndex[hex.EncodeToString(beaconStateKey.PublicKey[:])] = uint64(index)
	}
	return valKeyToIndex
}

// TODO: Move to utils
func BLSPubKeyToByte(blsKeys []phase0.BLSPubKey) [][]byte {
	keys := make([][]byte, 0)
	for i := range blsKeys {
		keys = append(keys, blsKeys[i][:])
	}
	return keys
}

// Make sure the validator indexes are active
func (p *BeaconState) PopulateParticipationAndBalance(
	poolName string,
	activeValidatorIndexes []uint64,
	beaconState *spec.VersionedBeaconState,
	prevBeaconState *spec.VersionedBeaconState,
	validatorIndexToWithdrawalAmount map[uint64]*big.Int) (schemas.ValidatorPerformanceMetrics, error) {

	metrics := schemas.ValidatorPerformanceMetrics{
		EarnedBalance:    big.NewInt(0),
		LosedBalance:     big.NewInt(0),
		TotalBalance:     big.NewInt(0),
		EffectiveBalance: big.NewInt(0),
		TotalRewards:     big.NewInt(0),
	}

	nOfIncorrectSource, nOfIncorrectTarget, nOfIncorrectHead, indexesMissedAtt := p.GetParticipation(
		activeValidatorIndexes,
		beaconState)

	currentBalance, currentEffectiveBalance := GetTotalBalanceAndEffective(activeValidatorIndexes, beaconState)
	prevBalance, prevEffectiveBalance := GetTotalBalanceAndEffective(activeValidatorIndexes, prevBeaconState)

	// Make sure we are comparing apples to apples
	effectiveBalanceDiff := new(big.Int).Sub(currentEffectiveBalance, prevEffectiveBalance)
	effectiveBalanceDiff.Abs(effectiveBalanceDiff)
	if effectiveBalanceDiff.Cmp(big.NewInt(0)) != 0 && effectiveBalanceDiff.Cmp(big.NewInt(1000000000)) < 0 { // > 0 && < 1 ETH
		return schemas.ValidatorPerformanceMetrics{},
			errors.New(fmt.Sprint("Can't calculate delta balances for pool: ", poolName, ", effective balances are different by less than 1 ETH:",
				currentEffectiveBalance, " vs ", prevEffectiveBalance))
	}

	rewards := big.NewInt(0).Sub(currentBalance, currentEffectiveBalance)
	deltaEpochBalance := big.NewInt(0).Sub(currentBalance, prevBalance)

	lessBalanceIndexes, earnedBalance, lostBalance, err := p.GetValidatorsWithLessBalance(
		activeValidatorIndexes,
		prevBeaconState,
		beaconState,
		validatorIndexToWithdrawalAmount)

	if err != nil {
		return schemas.ValidatorPerformanceMetrics{}, err
	}

	metrics.IndexesLessBalance = lessBalanceIndexes
	metrics.EarnedBalance = earnedBalance
	metrics.LosedBalance = lostBalance
	metrics.PoolName = poolName
	metrics.Time = time.Unix(int64(GetTimestamp(beaconState)), 0)

	metrics.Epoch = GetSlot(beaconState) / p.networkParameters.slotsInEpoch

	metrics.NOfTotalVotes = uint64(len(activeValidatorIndexes)) * 3
	metrics.NOfIncorrectSource = nOfIncorrectSource
	metrics.NOfIncorrectTarget = nOfIncorrectTarget
	metrics.NOfIncorrectHead = nOfIncorrectHead
	metrics.NOfValidatingKeys = uint64(len(activeValidatorIndexes))
	//metrics.NOfValsWithLessBalance = nOfValsWithDecreasedBalance
	//metrics.EarnedBalance = earned
	//metrics.LosedBalance = losed
	metrics.IndexesMissedAtt = indexesMissedAtt
	//metrics.LostBalanceKeys = lostKeys
	metrics.TotalBalance = currentBalance
	metrics.EffectiveBalance = currentEffectiveBalance
	metrics.TotalRewards = rewards
	metrics.DeltaEpochBalance = deltaEpochBalance

	return metrics, nil
}

// TODO: Get slashed validators

func (p *BeaconState) GetBeaconState(epoch uint64) (*spec.VersionedBeaconState, error) {
	log.Info("Fetching beacon state for epoch: ", epoch)
	// Its important to get the beacon state from the last slot of each epoch
	// to allow all attestations to be included
	// If epoch=1, slot = epoch*32 = 32, which is the first slot of epoch 1
	// but we want to run the metrics on the last slot, so -1
	// goes to the last slot of the previous epoch
	slotStr := strconv.FormatUint((epoch+1)*p.networkParameters.slotsInEpoch-1, 10)

	ctxTimeout, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(p.config.StateTimeout))
	defer cancel()
	opts := api.BeaconStateOpts{
		State: slotStr,
		// Override http client timeout
		Common: api.CommonOpts{
			Timeout: time.Second * time.Duration(p.config.StateTimeout),
		},
	}
	beaconState, err := p.consensus.BeaconState(
		ctxTimeout,
		&opts)
	if err != nil {
		return nil, err
	}
	log.Info("Got beacon state for epoch:", GetSlot(beaconState.Data)/p.networkParameters.slotsInEpoch)
	return beaconState.Data, nil
}

func GetTotalBalanceAndEffective(
	activeValidatorIndexes []uint64,
	beaconState *spec.VersionedBeaconState) (*big.Int, *big.Int) {

	totalBalances := big.NewInt(0).SetUint64(0)
	effectiveBalance := big.NewInt(0).SetUint64(0)
	validators := GetValidators(beaconState)
	balances := GetBalances(beaconState)

	for _, valIdx := range activeValidatorIndexes {
		// Skip if index is not present in the beacon state
		if valIdx >= uint64(len(balances)) {
			log.Warn("validator index goes beyond the beacon state indexes")
			continue
		}
		valBalance := big.NewInt(0).SetUint64(balances[valIdx])
		valEffBalance := big.NewInt(0).SetUint64(uint64(validators[valIdx].EffectiveBalance))
		totalBalances.Add(totalBalances, valBalance)
		effectiveBalance.Add(effectiveBalance, valEffBalance)
	}
	return totalBalances, effectiveBalance
}

// Returns the indexes of the validator keys. Note that the indexes
// may belong to active, inactive or even slashed keys.
func GetIndexesFromKeys(
	validatorKeys [][]byte,
	valKeyToIndex map[string]uint64) []uint64 {

	indexes := make([]uint64, 0)

	// Use global prepopulated map
	for _, key := range validatorKeys {
		if valIndex, ok := valKeyToIndex[hex.EncodeToString(key)]; ok {
			indexes = append(indexes, valIndex)
		} else {
			log.Warn("Index for key: ", hex.EncodeToString(key), " not found in beacon state")
		}
	}

	return indexes
}

func (p *BeaconState) GetActiveIndexes(
	validatorIndexes []uint64,
	beaconState *spec.VersionedBeaconState) []uint64 {

	activeIndexes := make([]uint64, 0)

	validators := GetValidators(beaconState)
	beaconStateEpoch := GetSlot(beaconState) / p.networkParameters.slotsInEpoch

	for _, valIdx := range validatorIndexes {
		if beaconStateEpoch >= uint64(validators[valIdx].ActivationEpoch) &&
			beaconStateEpoch < uint64(validators[valIdx].ExitEpoch) {
			activeIndexes = append(activeIndexes, valIdx)
		}
	}

	return activeIndexes
}

func (p *BeaconState) GetValidatorsWithLessBalance(
	activeValidatorIndexes []uint64,
	prevBeaconState *spec.VersionedBeaconState,
	currentBeaconState *spec.VersionedBeaconState,
	validatorIndexToWithdrawalAmount map[uint64]*big.Int) ([]uint64, *big.Int, *big.Int, error) {

	prevEpoch := GetSlot(prevBeaconState) / p.networkParameters.slotsInEpoch
	currEpoch := GetSlot(currentBeaconState) / p.networkParameters.slotsInEpoch
	prevBalances := GetBalances(prevBeaconState)
	currBalances := GetBalances(currentBeaconState)

	if (prevEpoch + 1) != currEpoch {
		return nil, nil, nil, errors.New(fmt.Sprintf(
			"epochs are not consecutive: slot %d vs %d",
			prevEpoch,
			currEpoch))
	}

	indexesWithLessBalance := make([]uint64, 0)
	earnedBalance := big.NewInt(0)
	lostBalance := big.NewInt(0)

	for _, valIdx := range activeValidatorIndexes {
		// handle if there was a new validator index not register in the prev state
		if valIdx >= uint64(len(prevBalances)) {
			log.Warn("validator index goes beyond the beacon state indexes")
			continue
		}

		prevEpochValBalance := big.NewInt(0).SetUint64(prevBalances[valIdx])
		currentEpochValBalance := big.NewInt(0).SetUint64(currBalances[valIdx])
		if valWithdrawalAmount, ok := validatorIndexToWithdrawalAmount[valIdx]; ok {
			currentEpochValBalance.Add(currentEpochValBalance, valWithdrawalAmount)
		}
		delta := big.NewInt(0).Sub(currentEpochValBalance, prevEpochValBalance)

		if delta.Cmp(big.NewInt(0)) == -1 {
			indexesWithLessBalance = append(indexesWithLessBalance, valIdx)
			lostBalance.Add(lostBalance, delta)
		} else {
			earnedBalance.Add(earnedBalance, delta)
		}
	}

	return indexesWithLessBalance, earnedBalance, lostBalance, nil
}

func (p *BeaconState) ParticipationDebug(
	activeValidatorIndexes []uint64,
	beaconState *spec.VersionedBeaconState) {

	validators := GetValidators(beaconState)
	previousEpochParticipation := GetPreviousEpochParticipation(beaconState)

	nActiveValidators := uint64(0)

	beaconStateEpoch := GetSlot(beaconState) / p.networkParameters.slotsInEpoch

	var nCorrectSource, nCorrectTarget, nCorrectHead uint64

	for _, valIndx := range activeValidatorIndexes {
		// Ignore slashed validators
		if validators[valIndx].Slashed {
			continue
		}

		// Ignore not yet active validators
		if uint64(validators[valIndx].ActivationEpoch) > beaconStateEpoch {
			continue
		}

		epochAttestations := previousEpochParticipation[valIndx]
		if isBitSet(uint8(epochAttestations), 0) {
			nCorrectSource++
		}
		if isBitSet(uint8(epochAttestations), 1) {
			nCorrectTarget++
		}
		if isBitSet(uint8(epochAttestations), 2) {
			nCorrectHead++
		}
		nActiveValidators++
	}

	log.Info("Active validators: ", nActiveValidators)
	log.Info("Correct Source: ", (float64(nCorrectSource) / float64(nActiveValidators) * 100))
	log.Info("Correct Target: ", (float64(nCorrectTarget) / float64(nActiveValidators) * 100))
	log.Info("Correct Head: ", (float64(nCorrectHead) / float64(nActiveValidators) * 100))
}

// See spec: from LSB to MSB: source, target, head.
// https://github.com/ethereum/consensus-specs/blob/master/specs/altair/beacon-chain.md#participation-flag-indices
func (p *BeaconState) GetParticipation(
	activeValidatorIndexes []uint64,
	beaconState *spec.VersionedBeaconState) (uint64, uint64, uint64, []uint64) {

	indexesMissedAtt := make([]uint64, 0)

	validators := GetValidators(beaconState)
	previousEpochParticipation := GetPreviousEpochParticipation(beaconState)

	var nIncorrectSource, nIncorrectTarget, nIncorrectHead uint64

	for _, valIndx := range activeValidatorIndexes {
		// Ignore slashed validators
		if validators[valIndx].Slashed {
			continue
		}
		beaconStateEpoch := GetSlot(beaconState) / p.networkParameters.slotsInEpoch
		// Ignore not yet active validators
		// TODO: Test this
		if uint64(validators[valIndx].ActivationEpoch) > beaconStateEpoch {
			//log.Warn("index: ", valIndx, " is not active yet")
			continue
		}

		// TODO: Dont know why but Infura returns 0 for all CurrentEpochAttestations
		epochAttestations := previousEpochParticipation[valIndx]
		// TODO: Count if bit is set instead if not set. Easier.
		if !isBitSet(uint8(epochAttestations), 0) {
			nIncorrectSource++
			indexesMissedAtt = append(indexesMissedAtt, valIndx)
		}
		if !isBitSet(uint8(epochAttestations), 1) {
			nIncorrectTarget++
		}
		if !isBitSet(uint8(epochAttestations), 2) {
			nIncorrectHead++
		}
	}
	return nIncorrectSource, nIncorrectTarget, nIncorrectHead, indexesMissedAtt
}

// Check if bit n (0..7) is set where 0 is the LSB in little endian
func isBitSet(input uint8, n int) bool {
	return (input & (1 << n)) > uint8(0)
}

func logMetrics(
	metrics schemas.ValidatorPerformanceMetrics,
	poolName string) {
	balanceDecreasedPercent := (float64(len(metrics.IndexesLessBalance)) / float64(metrics.NOfValidatingKeys)) * 100

	log.WithFields(log.Fields{
		"PoolName":                    poolName,
		"Epoch":                       metrics.Epoch,
		"nOfActiveValidators":         metrics.NOfActiveValidators,
		"nOfTotalVotes":               metrics.NOfTotalVotes,
		"nOfIncorrectSource":          metrics.NOfIncorrectSource,
		"nOfIncorrectTarget":          metrics.NOfIncorrectTarget,
		"nOfIncorrectHead":            metrics.NOfIncorrectHead,
		"nOfValidators":               metrics.NOfValidatingKeys,
		"PercentIncorrectSource":      (float64(metrics.NOfIncorrectSource) / float64(metrics.NOfValidatingKeys)) * 100,
		"PercentIncorrectTarget":      (float64(metrics.NOfIncorrectTarget) / float64(metrics.NOfValidatingKeys)) * 100,
		"PercentIncorrectHead":        (float64(metrics.NOfIncorrectHead) / float64(metrics.NOfValidatingKeys)) * 100,
		"nOfValsWithDecreasedBalance": len(metrics.IndexesLessBalance),
		"balanceDecreasedPercent":     balanceDecreasedPercent,
		"epochEarnedBalance":          metrics.EarnedBalance,
		"epochLostBalance":            metrics.LosedBalance,
		"totalBalance":                metrics.TotalBalance,
		"effectiveBalance":            metrics.EffectiveBalance,
		"totalRewards":                metrics.TotalRewards,
		"ValidadorKeyMissedAtt":       metrics.IndexesMissedAtt,
		"ValidadorKeyLessBalance":     metrics.IndexesLessBalance,
		"DeltaEpochBalance":           metrics.DeltaEpochBalance,
		"epochMEVRewards":             metrics.MEVRewards,
	}).Info(poolName + " Stats:")
}

// Wrappers on top of the beacon state to fetch some fields regardless of Altair or Bellatrix
// Note that this is needed because both block types do not implement the same interface, since
// the state differs accross versions.
// Note also that this functions only make sense for the beacon state fields that are common
// to all the versioned beacon states.
func GetValidators(beaconState *spec.VersionedBeaconState) []*phase0.Validator {
	var validators []*phase0.Validator
	if beaconState.Altair != nil {
		validators = beaconState.Altair.Validators
	} else if beaconState.Bellatrix != nil {
		validators = beaconState.Bellatrix.Validators
	} else if beaconState.Capella != nil {
		validators = beaconState.Capella.Validators
	} else if beaconState.Deneb != nil {
		validators = beaconState.Deneb.Validators
	} else if beaconState.Electra != nil {
		validators = beaconState.Electra.Validators
	} else if beaconState.Fulu != nil {
		validators = beaconState.Fulu.Validators
	} else {
		log.Fatal("Beacon state was empty")
	}
	return validators
}

func GetBalances(beaconState *spec.VersionedBeaconState) []uint64 {
	var tmpBalances []phase0.Gwei
	if beaconState.Altair != nil {
		tmpBalances = beaconState.Altair.Balances
	} else if beaconState.Bellatrix != nil {
		tmpBalances = beaconState.Bellatrix.Balances
	} else if beaconState.Capella != nil {
		tmpBalances = beaconState.Capella.Balances
	} else if beaconState.Deneb != nil {
		tmpBalances = beaconState.Deneb.Balances
	} else if beaconState.Electra != nil {
		tmpBalances = beaconState.Electra.Balances
	} else if beaconState.Fulu != nil {
		tmpBalances = beaconState.Fulu.Balances
	} else {
		log.Fatal("Beacon state was empty")
	}

	balances := make([]uint64, len(tmpBalances))
	for i := range tmpBalances {
		balances[i] = uint64(tmpBalances[i])
	}
	return balances
}

func GetPreviousEpochParticipation(beaconState *spec.VersionedBeaconState) []altair.ParticipationFlags {
	var previousEpochParticipation []altair.ParticipationFlags
	if beaconState.Altair != nil {
		previousEpochParticipation = beaconState.Altair.PreviousEpochParticipation
	} else if beaconState.Bellatrix != nil {
		previousEpochParticipation = beaconState.Bellatrix.PreviousEpochParticipation
	} else if beaconState.Capella != nil {
		previousEpochParticipation = beaconState.Capella.PreviousEpochParticipation
	} else if beaconState.Deneb != nil {
		previousEpochParticipation = beaconState.Deneb.PreviousEpochParticipation
	} else if beaconState.Electra != nil {
		previousEpochParticipation = beaconState.Electra.PreviousEpochParticipation
	} else if beaconState.Fulu != nil {
		previousEpochParticipation = beaconState.Fulu.PreviousEpochParticipation
	} else {
		log.Fatal("Beacon state was empty")
	}
	return previousEpochParticipation
}

func GetSlot(beaconState *spec.VersionedBeaconState) uint64 {
	var slot uint64
	if beaconState.Altair != nil {
		slot = uint64(beaconState.Altair.Slot)
	} else if beaconState.Bellatrix != nil {
		slot = uint64(beaconState.Bellatrix.Slot)
	} else if beaconState.Capella != nil {
		slot = uint64(beaconState.Capella.Slot)
	} else if beaconState.Deneb != nil {
		slot = uint64(beaconState.Deneb.Slot)
	} else if beaconState.Electra != nil {
		slot = uint64(beaconState.Electra.Slot)
	} else if beaconState.Fulu != nil {
		slot = uint64(beaconState.Fulu.Slot)
	} else {
		log.Fatal("Beacon state was empty")
	}
	return slot
}

func GetTimestamp(beaconState *spec.VersionedBeaconState) uint64 {
	var timestamp uint64
	if beaconState.Bellatrix != nil {
		timestamp = uint64(beaconState.Bellatrix.LatestExecutionPayloadHeader.Timestamp)
	} else if beaconState.Capella != nil {
		timestamp = uint64(beaconState.Capella.LatestExecutionPayloadHeader.Timestamp)
	} else if beaconState.Deneb != nil {
		timestamp = uint64(beaconState.Deneb.LatestExecutionPayloadHeader.Timestamp)
	} else if beaconState.Electra != nil {
		timestamp = uint64(beaconState.Electra.LatestExecutionPayloadHeader.Timestamp)
	} else if beaconState.Fulu != nil {
		timestamp = uint64(beaconState.Fulu.LatestExecutionPayloadHeader.Timestamp)
	} else {
		log.Fatal("Could not get timestamp from beacon state")
	}
	return timestamp
}

func GetCurrentSyncCommittee(beaconState *spec.VersionedBeaconState) []phase0.BLSPubKey {
	var pubKeys []phase0.BLSPubKey
	if beaconState.Altair != nil {
		pubKeys = beaconState.Altair.CurrentSyncCommittee.Pubkeys
	} else if beaconState.Bellatrix != nil {
		pubKeys = beaconState.Bellatrix.CurrentSyncCommittee.Pubkeys
	} else if beaconState.Capella != nil {
		pubKeys = beaconState.Capella.CurrentSyncCommittee.Pubkeys
	} else if beaconState.Deneb != nil {
		pubKeys = beaconState.Deneb.CurrentSyncCommittee.Pubkeys
	} else if beaconState.Electra != nil {
		pubKeys = beaconState.Electra.CurrentSyncCommittee.Pubkeys
	} else if beaconState.Fulu != nil {
		pubKeys = beaconState.Fulu.CurrentSyncCommittee.Pubkeys
	} else {
		log.Fatal("Beacon state was empty")
	}
	return pubKeys
}
