package metrics

import (
	"time"

	"github.com/attestantio/go-eth2-client/http"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/bilinearlabs/eth-metrics/db"
	"github.com/bilinearlabs/eth-metrics/schemas"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type NetworkStats struct {
	consensus         *http.Service
	networkParameters *NetworkParameters
	database          *db.Database
	config            *config.Config
}

func NewNetworkStats(
	consensus *http.Service,
	networkParameters *NetworkParameters,
	database *db.Database,
	config *config.Config) (*NetworkStats, error) {
	return &NetworkStats{
		consensus:         consensus,
		networkParameters: networkParameters,
		database:          database,
		config:            config,
	}, nil
}

func (n *NetworkStats) Run(
	currentEpoch uint64,
	currentBeaconState *spec.VersionedBeaconState,
) error {
	if currentBeaconState == nil {
		return errors.New("current beacon state is nil")
	}

	networkStats, err := n.GetNetworkStats(currentEpoch, currentBeaconState)
	if err != nil {
		return errors.Wrap(err, "error getting network stats")
	}

	if n.database != nil {
		err = n.database.StoreNetworkMetrics(networkStats)
		if err != nil {
			return errors.Wrap(err, "could not store network stats")
		}
	}

	return nil
}

func (n *NetworkStats) GetNetworkStats(
	currentEpoch uint64,
	beaconState *spec.VersionedBeaconState,
) (schemas.NetworkStats, error) {
	networkStats := schemas.NetworkStats{
		Time:                 time.Unix(int64(GetTimestamp(beaconState)), 0),
		Epoch:                currentEpoch,
		NOfActiveValidators:  0,
		NOfExitedValidators:  0,
		NOfSlashedValidators: 0,
	}
	validators := GetValidators(beaconState)

	for _, val := range validators {
		if val.Slashed {
			networkStats.NOfSlashedValidators++
		}
		if uint64(val.ExitEpoch) <= currentEpoch {
			networkStats.NOfExitedValidators++
		} else if uint64(val.ActivationEpoch) <= currentEpoch {
			networkStats.NOfActiveValidators++
		}
	}

	log.WithFields(log.Fields{
		"Total Validators":         len(validators),
		"Total Slashed Validators": networkStats.NOfSlashedValidators,
		"Total Exited Validators":  networkStats.NOfExitedValidators,
		"Total Active Validators":  networkStats.NOfActiveValidators,
	}).Info("Network stats:")

	return networkStats, nil
}
