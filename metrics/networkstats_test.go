package metrics

import (
	"testing"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/fulu"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/bilinearlabs/eth-metrics/db"
	"github.com/stretchr/testify/assert"
)

func TestGetNetworkStats_Success(t *testing.T) {
	networkStats, err := NewNetworkStats(&db.Database{})
	if err != nil {
		t.Fatalf("Error creating network stats: %v", err)
	}
	// 1 slashed validator, 2 exited validators, 1 active validator
	validator_0 := ToBytes48([]byte{10})
	validator_1 := ToBytes48([]byte{20})
	validator_2 := ToBytes48([]byte{30})
	beaconState := &spec.VersionedBeaconState{
		Fulu: &fulu.BeaconState{
			Validators: []*phase0.Validator{
				{
					PublicKey:       validator_0,
					Slashed:         true,
					ExitEpoch:       1,
					ActivationEpoch: 0,
				},
				{
					PublicKey:       validator_1,
					Slashed:         false,
					ExitEpoch:       0,
					ActivationEpoch: 0,
				},
				{
					PublicKey:       validator_2,
					Slashed:         false,
					ExitEpoch:       2,
					ActivationEpoch: 0,
				},
			},
			LatestExecutionPayloadHeader: &deneb.ExecutionPayloadHeader{
				Timestamp: 1673308800,
			},
		},
	}
	networkStatsResult, err := networkStats.GetNetworkStats(1, beaconState)
	assert.NoError(t, err)
	assert.Equal(t, uint64(1), networkStatsResult.NOfSlashedValidators)
	assert.Equal(t, uint64(2), networkStatsResult.NOfExitedValidators)
	assert.Equal(t, uint64(1), networkStatsResult.NOfActiveValidators)
	assert.NotNil(t, networkStatsResult)
}
