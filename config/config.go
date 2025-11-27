package config

import (
	"flag"
	"os"

	log "github.com/sirupsen/logrus"
)

// By default the release is a custom build. CI takes care of upgrading it with
// go build -v -ldflags="-X 'github.com/bilinearlabs/eth-metrics/config.ReleaseVersion=x.y.z'"
var ReleaseVersion = "custom-build"

type Config struct {
	PoolNames      []string
	ValidatorsFile string
	DatabasePath   string
	Eth1Address    string
	Eth2Address    string
	EpochDebug     string
	Verbosity      string
	Network        string
	Credentials    string
	BackfillEpochs uint64
	StateTimeout   int
}

// custom implementation to allow providing the same flag multiple times
// --flag=value1 --flag=value2
type arrayFlags []string

func (i *arrayFlags) String() string {
	return ""
}

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func NewCliConfig() (*Config, error) {
	var poolNames arrayFlags

	// Allows passing multiple times
	flag.Var(&poolNames, "pool-name", "Pool name to monitor. Can be useed multiple times")

	var validatorsFile = flag.String("validators-file", "", "csv file with entities and their validator keys")
	var version = flag.Bool("version", false, "Prints the release version and exits")
	var network = flag.String("network", "ethereum", "ethereum|gnosis")
	var databasePath = flag.String("database-path", "", "Database path: db.db (optional)")
	var eth1Address = flag.String("eth1address", "", "Ethereum 1 http endpoint. To be used by rocket pool")
	var eth2Address = flag.String("eth2address", "", "Ethereum 2 http endpoint")
	var stateTimeout = flag.Int("state-timeout", 60, "Timeout in seconds for fetching the beacon state")
	var epochDebug = flag.String("epoch-debug", "", "Calculates the stats for a given epoch and exits, useful for debugging")
	var verbosity = flag.String("verbosity", "info", "Logging verbosity (trace, debug, info=default, warn, error, fatal, panic)")
	var credentials = flag.String("credentials", "", "Credentials for the http client (username:password)")
	var backfillEpochs = flag.Uint64("backfill-epochs", 0, "Number of epochs to backfill")

	flag.Parse()

	if *version {
		log.Info("Version: ", ReleaseVersion)
		os.Exit(0)
	}

	conf := &Config{
		PoolNames:      poolNames,
		ValidatorsFile: *validatorsFile,
		DatabasePath:   *databasePath,
		Eth1Address:    *eth1Address,
		Eth2Address:    *eth2Address,
		EpochDebug:     *epochDebug,
		Verbosity:      *verbosity,
		Network:        *network,
		Credentials:    *credentials,
		BackfillEpochs: *backfillEpochs,
		StateTimeout:   *stateTimeout,
	}
	logConfig(conf)
	return conf, nil
}

func logConfig(cfg *Config) {
	log.WithFields(log.Fields{
		"PoolNames":      cfg.PoolNames,
		"ValidatorsFile": cfg.ValidatorsFile,
		"DatabasePath":   cfg.DatabasePath,
		"Eth1Address":    cfg.Eth1Address,
		"Eth2Address":    cfg.Eth2Address,
		"EpochDebug":     cfg.EpochDebug,
		"Verbosity":      cfg.Verbosity,
		"Network":        cfg.Network,
		"Credentials":    "***",
		"BackfillEpochs": cfg.BackfillEpochs,
		"StateTimeout":   cfg.StateTimeout,
	}).Info("Cli Config:")
}
