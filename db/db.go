package db

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/bilinearlabs/eth-metrics/schemas"
	"github.com/pkg/errors"
	_ "modernc.org/sqlite"
)

var createPoolsMetricsTable = `
CREATE TABLE IF NOT EXISTS t_pools_metrics_summary (
     f_timestamp TIMESTAMPTZ NOT NULL,
	 f_epoch BIGINT,
	 f_pool TEXT,
	 f_epoch_timestamp TIMESTAMPTZ NOT NULL,

	 f_n_total_votes BIGINT,
	 f_n_incorrect_source BIGINT,
	 f_n_incorrect_target BIGINT,
	 f_n_incorrect_head BIGINT,
	 f_n_validating_keys BIGINT,
	 f_n_valitadors_with_less_balace BIGINT,
	 f_epoch_earned_balance_gwei BIGINT,
	 f_epoch_lost_balace_gwei BIGINT,
	 f_mev_rewards_wei BIGINT,

	 f_n_scheduled_blocks BIGINT,
	 f_n_proposed_blocks BIGINT,

	 PRIMARY KEY (f_epoch, f_pool)
);
`

var createProposalDutiesTable = `
CREATE TABLE IF NOT EXISTS t_proposal_duties (
	 f_epoch BIGINT,
	 f_pool TEXT,
	 f_n_scheduled_blocks BIGINT,
	 f_n_proposed_blocks BIGINT,
	 PRIMARY KEY (f_epoch, f_pool)
);
`

var createEthPriceTable = `
CREATE TABLE IF NOT EXISTS t_eth_price (
	 f_timestamp TIMESTAMPTZ NOT NULL PRIMARY KEY,
	 f_eth_price_usd FLOAT
);
`

var insertEthPrice = `
INSERT INTO t_eth_price(
	f_timestamp,
	f_eth_price_usd)
VALUES (?, ?)
ON CONFLICT (f_timestamp)
DO UPDATE SET
   f_eth_price_usd=EXCLUDED.f_eth_price_usd
`

// TODO: Add missing
// MissedAttestationsKeys []string
// LostBalanceKeys        []string
var insertValidatorPerformance = `
INSERT INTO t_pools_metrics_summary(
	f_timestamp,
	f_epoch,
	f_pool,
	f_epoch_timestamp,
	f_n_total_votes,
	f_n_incorrect_source,
	f_n_incorrect_target,
	f_n_incorrect_head,
	f_n_validating_keys,
	f_n_valitadors_with_less_balace,
	f_epoch_earned_balance_gwei,
	f_epoch_lost_balace_gwei,
	f_mev_rewards_wei)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (f_epoch, f_pool)
DO UPDATE SET
   f_timestamp=EXCLUDED.f_timestamp,
   f_epoch_timestamp=EXCLUDED.f_epoch_timestamp,
   f_n_total_votes=EXCLUDED.f_n_total_votes,
	 f_n_incorrect_source=EXCLUDED.f_n_incorrect_source,
	 f_n_incorrect_target=EXCLUDED.f_n_incorrect_target,
	 f_n_incorrect_head=EXCLUDED.f_n_incorrect_head,
	 f_n_validating_keys=EXCLUDED.f_n_validating_keys,
	 f_n_valitadors_with_less_balace=EXCLUDED.f_n_valitadors_with_less_balace,
	 f_epoch_earned_balance_gwei=EXCLUDED.f_epoch_earned_balance_gwei,
	 f_epoch_lost_balace_gwei=EXCLUDED.f_epoch_lost_balace_gwei,
	 f_mev_rewards_wei=EXCLUDED.f_mev_rewards_wei
`

// TODO: Add f_epoch_timestamp
var insertProposalDuties = `
INSERT INTO t_proposal_duties(
	f_epoch,
	f_pool,
	f_n_scheduled_blocks,
	f_n_proposed_blocks)
VALUES (?, ?, ?, ?)
ON CONFLICT (f_epoch, f_pool)
DO UPDATE SET
   f_n_scheduled_blocks=EXCLUDED.f_n_scheduled_blocks,
   f_n_proposed_blocks=EXCLUDED.f_n_proposed_blocks
`

type Database struct {
	db       *sql.DB
	PoolName string
}

func New(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	return &Database{
		db: db,
	}, nil
}

func (a *Database) CreateTables() error {
	if _, err := a.db.ExecContext(
		context.Background(),
		createPoolsMetricsTable); err != nil {
		return err
	}

	if _, err := a.db.ExecContext(
		context.Background(),
		createProposalDutiesTable); err != nil {
		return err
	}

	return nil
}

func (a *Database) CreateEthPriceTable() error {
	if _, err := a.db.ExecContext(
		context.Background(),
		createEthPriceTable); err != nil {
		return err
	}
	return nil
}

func (a *Database) StoreProposalDuties(epoch uint64, poolName string, scheduledBlocks uint64, proposedBlocks uint64) error {
	_, err := a.db.ExecContext(
		context.Background(),
		insertProposalDuties,
		epoch,
		poolName,
		scheduledBlocks,
		proposedBlocks)

	if err != nil {
		return err
	}
	return nil
}

func (a *Database) StoreValidatorPerformance(validatorPerformance schemas.ValidatorPerformanceMetrics) error {
	_, err := a.db.ExecContext(
		context.Background(),
		insertValidatorPerformance,
		validatorPerformance.Time,
		validatorPerformance.Epoch,
		validatorPerformance.PoolName,
		validatorPerformance.Time,
		validatorPerformance.NOfTotalVotes,
		validatorPerformance.NOfIncorrectSource,
		validatorPerformance.NOfIncorrectTarget,
		validatorPerformance.NOfIncorrectHead,
		validatorPerformance.NOfValidatingKeys,
		validatorPerformance.NOfValsWithLessBalance,
		validatorPerformance.EarnedBalance.Int64(),
		validatorPerformance.LosedBalance.Int64(),
		validatorPerformance.MEVRewards.Int64(),
	)

	if err != nil {
		return err
	}
	return nil
}

func (a *Database) StoreEthPrice(ethPriceUsd float32) error {
	_, err := a.db.ExecContext(
		context.Background(),
		insertEthPrice,
		time.Now(), // not really correct
		ethPriceUsd)

	if err != nil {
		return err
	}
	return nil
}

func (a *Database) GetMissingEpochs(currentEpoch uint64, backfillEpochs uint64) ([]uint64, error) {
	// Generate the expected range of epochs
	expectedEpochs := make(map[uint64]bool)
	for epoch := currentEpoch - backfillEpochs + 1; epoch <= currentEpoch; epoch++ {
		expectedEpochs[epoch] = true
	}

	// Query existing epochs in the range
	query := `
		SELECT f_epoch
		FROM t_pools_metrics_summary
		WHERE f_epoch BETWEEN ? AND ?
	`

	rows, err := a.db.QueryContext(context.Background(), query, currentEpoch-backfillEpochs+1, currentEpoch)
	if err != nil {
		return nil, errors.Wrap(err, "could not get existing epochs")
	}

	defer rows.Close()
	for rows.Next() {
		var epoch uint64
		if err := rows.Scan(&epoch); err != nil {
			return nil, err
		}
		delete(expectedEpochs, epoch)
	}

	// Collect missing epochs
	missingEpochs := make([]uint64, 0, len(expectedEpochs))
	for epoch := range expectedEpochs {
		missingEpochs = append(missingEpochs, epoch)
	}

	// Sort the missing epochs in descending order
	sort.Slice(missingEpochs, func(i, j int) bool { return missingEpochs[i] < missingEpochs[j] })

	return missingEpochs, nil
}
