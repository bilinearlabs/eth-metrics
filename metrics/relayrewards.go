package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/flashbots/mev-boost-relay/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

var RELAY_SERVERS = []string{
	"https://relay-analytics.ultrasound.money",
	"https://titanrelay.xyz",
	"https://bloxroute.max-profit.blxrbdn.com",
	"https://bloxroute.regulated.blxrbdn.com",
	"https://boost-relay.flashbots.net",
	"https://aestus.live",
	"https://agnostic-relay.net",
	"https://relay.ethgas.com",
	"https://relay.btcs.com",
}

type RelayRewards struct {
	httpClient         *http.Client
	networkParameters  *NetworkParameters
	validatorKeyToPool map[string]string
	config             *config.Config
}

func NewRelayRewards(
	networkParameters *NetworkParameters,
	validatorKeyToPool map[string]string,
	config *config.Config) (*RelayRewards, error) {
	return &RelayRewards{
		httpClient:         &http.Client{Timeout: 60 * time.Second},
		networkParameters:  networkParameters,
		validatorKeyToPool: validatorKeyToPool,
		config:             config,
	}, nil
}

func (r *RelayRewards) GetRelayRewards(
	epoch uint64,
) (map[string]*big.Int, error) {
	slotsInEpoch := r.networkParameters.slotsInEpoch
	poolRewards := make(map[string]*big.Int)

	results := make(chan struct {
		pool   string
		reward *big.Int
	}, len(RELAY_SERVERS)*int(slotsInEpoch))
	var wg sync.WaitGroup
	var consumerWg sync.WaitGroup

	// Consumer
	consumerWg.Add(1)
	go func() {
		defer consumerWg.Done()
		for result := range results {
			if _, ok := poolRewards[result.pool]; !ok {
				poolRewards[result.pool] = big.NewInt(0)
			}
			poolRewards[result.pool] = new(big.Int).Add(poolRewards[result.pool], result.reward)
		}
	}()

	for i := range slotsInEpoch {
		// Wait to avoid rate limiting
		time.Sleep(250 * time.Millisecond)
		slot := epoch*slotsInEpoch + i
		for _, relayServer := range RELAY_SERVERS {
			wg.Add(1)
			go func(relayServer string, slot uint64) {
				defer wg.Done()
				payloads, err := r.getRewards(relayServer, slot)

				if err != nil {
					log.Errorf("error getting rewards from %s: %s", relayServer, err)
					return
				}

				for _, payload := range payloads {
					pool, ok := r.validatorKeyToPool[payload.ProposerPubkey]
					if !ok {
						continue
					}
					// bigint
					value, ok := big.NewInt(0).SetString(payload.Value, 10)
					if !ok {
						log.Errorf("failed to parse value: %s", payload.Value)
						continue
					}
					results <- struct {
						pool   string
						reward *big.Int
					}{pool, value}
				}
			}(relayServer, slot)
		}
	}
	wg.Wait()
	close(results)
	consumerWg.Wait()

	return poolRewards, nil
}

func (r *RelayRewards) getRewards(relayServer string, slot uint64) ([]common.BidTraceV2JSON, error) {
	resp, err := r.httpClient.Get(fmt.Sprintf("%s/relay/v1/data/bidtraces/proposer_payload_delivered?slot=%d", relayServer, slot))
	if err != nil {
		return nil, errors.Wrap(err, "error getting rewards")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(fmt.Sprintf("non-200 status: %d", resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "error reading response body")
	}
	var payloads []common.BidTraceV2JSON

	if err := json.Unmarshal(body, &payloads); err != nil {
		return nil, errors.Wrap(err, "error decoding proposer payload delivered")
	}

	return payloads, nil
}
