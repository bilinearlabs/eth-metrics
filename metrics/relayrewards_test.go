package metrics

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bilinearlabs/eth-metrics/config"
	"github.com/flashbots/mev-boost-relay/common"
	"github.com/stretchr/testify/assert"
)

func TestGetRelayRewards_Success(t *testing.T) {
	// Create a test server that returns valid rewards
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request path
		assert.Contains(t, r.URL.Path, "/relay/v1/data/bidtraces/proposer_payload_delivered")

		// Return mock rewards data
		payloads := []common.BidTraceV2JSON{
			{
				ProposerPubkey: "0x1234567890abcdef",
				Value:          "1000000000000000000",
			},
			{
				ProposerPubkey: "0xabcdef1234567890",
				Value:          "2000000000000000000",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payloads)
	}))
	defer server.Close()

	RELAY_SERVERS = []string{server.URL}

	networkParams := &NetworkParameters{
		slotsInEpoch: 2,
	}
	validatorKeyToPool := map[string]string{
		"0x1234567890abcdef": "pool1",
		"0xabcdef1234567890": "pool2",
	}
	cfg := &config.Config{}

	relayRewards, err := NewRelayRewards(networkParams, validatorKeyToPool, cfg)
	assert.NoError(t, err)

	// Call GetRelayRewards
	rewards, err := relayRewards.GetRelayRewards(0)
	assert.NoError(t, err)
	assert.NotNil(t, rewards)

	// Verify rewards are aggregated correctly
	// Each slot (2 slots) * each relay server (1 server) = 2 requests
	// pool1: 2 * 1 ETH = 2 ETH
	// pool2: 2 * 2 ETH = 4 ETH
	assert.Equal(t, big.NewInt(2000000000000000000), rewards["pool1"])
	assert.Equal(t, big.NewInt(4000000000000000000), rewards["pool2"])
}

func TestGetRelayRewards_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	RELAY_SERVERS = []string{server.URL}

	networkParams := &NetworkParameters{
		slotsInEpoch: 1,
	}
	validatorKeyToPool := map[string]string{
		"0x1234567890abcdef": "pool1",
	}
	cfg := &config.Config{}

	relayRewards, err := NewRelayRewards(networkParams, validatorKeyToPool, cfg)
	assert.NoError(t, err)

	rewards, err := relayRewards.GetRelayRewards(0)
	assert.Error(t, err)
	assert.Nil(t, rewards)
}

func TestGetRelayRewards_InvalidValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"proposer_pubkey": "0x1234567890abcdef", "value": "Invalid Value"}]`))
	}))
	defer server.Close()

	RELAY_SERVERS = []string{server.URL}

	networkParams := &NetworkParameters{
		slotsInEpoch: 1,
	}
	validatorKeyToPool := map[string]string{
		"0x1234567890abcdef": "pool1",
	}
	cfg := &config.Config{}

	relayRewards, err := NewRelayRewards(networkParams, validatorKeyToPool, cfg)
	assert.NoError(t, err)

	rewards, err := relayRewards.GetRelayRewards(0)
	assert.Error(t, err)
	assert.Nil(t, rewards)
}
