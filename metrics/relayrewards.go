package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/flashbots/mev-boost-relay/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
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
	retryOpts          []retry.Option
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
		retryOpts: []retry.Option{
			retry.Attempts(5),
			retry.Delay(5 * time.Second),
		},
	}, nil
}

func (r *RelayRewards) GetRelayRewards(
	epoch uint64,
) (map[string]*big.Int, map[uint64]struct{}, error) {
	slotsInEpoch := r.networkParameters.slotsInEpoch
	poolRewards := make(map[string]*big.Int)
	slotsWithRewards := make(map[uint64]struct{})

	results := make(chan struct {
		slot   uint64
		pool   string
		reward *big.Int
	})
	var g errgroup.Group
	var consumerWg sync.WaitGroup

	// Create per-relay semaphores (limit to 1 concurrent request per relay)
	relaySem := make(map[string]chan struct{})
	for _, relay := range RELAY_SERVERS {
		relaySem[relay] = make(chan struct{}, 1)
	}

	// Consumer
	consumerWg.Go(func() {
		for result := range results {
			if _, ok := poolRewards[result.pool]; !ok {
				poolRewards[result.pool] = big.NewInt(0)
			}
			poolRewards[result.pool] = new(big.Int).Add(poolRewards[result.pool], result.reward)
			slotsWithRewards[result.slot] = struct{}{}
		}
	})

	for i := range slotsInEpoch {
		slot := epoch*slotsInEpoch + i
		for _, relayServer := range RELAY_SERVERS {
			g.Go(func() error {
				// Acquire semaphore for this relay (blocks if another request is in progress)
				relaySem[relayServer] <- struct{}{}
				defer func() { <-relaySem[relayServer] }()

				payloads, err := r.getRewards(relayServer, slot)
				if err != nil {
					return errors.Wrap(err, fmt.Sprintf("error getting rewards from %s", relayServer))
				}
				for _, payload := range payloads {
					pool, ok := r.validatorKeyToPool[payload.ProposerPubkey]
					if !ok {
						continue
					}
					value, ok := big.NewInt(0).SetString(payload.Value, 10)
					if !ok {
						return errors.New(fmt.Sprintf("failed to parse value: %s", payload.Value))
					}
					results <- struct {
						slot   uint64
						pool   string
						reward *big.Int
					}{slot, pool, value}
				}
				return nil
			})
		}
	}
	if err := g.Wait(); err != nil {
		close(results)
		consumerWg.Wait()
		return nil, nil, errors.Wrap(err, "error getting rewards")
	}
	close(results)
	consumerWg.Wait()

	return poolRewards, slotsWithRewards, nil
}

func (r *RelayRewards) getRewards(relayServer string, slot uint64) ([]common.BidTraceV2JSON, error) {
	var body []byte

	err := retry.Do(func() error {
		resp, err := r.httpClient.Get(fmt.Sprintf("%s/relay/v1/data/bidtraces/proposer_payload_delivered?slot=%d", relayServer, slot))
		if err != nil {
			log.Warnf("error getting rewards from %s: %s. Slot: %d. Retrying...", relayServer, err, slot)
			return errors.Wrap(err, "error getting rewards from "+relayServer)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Warnf("non-200 status from %s: %d. Slot: %d. Retrying...", relayServer, resp.StatusCode, slot)
			return errors.New(fmt.Sprintf("non-200 status: %d", resp.StatusCode))
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "error reading response body")
		}
		return nil
	}, r.retryOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "error getting rewards")
	}
	var payloads []common.BidTraceV2JSON

	if err := json.Unmarshal(body, &payloads); err != nil {
		return nil, errors.Wrap(err, "error decoding proposer payload delivered")
	}

	return payloads, nil
}
