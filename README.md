# eth-metrics

[![Tag](https://img.shields.io/github/tag/bilinearlabs/eth-metrics.svg)](https://github.com/bilinearlabs/eth-metrics/releases/)
[![Release](https://github.com/bilinearlabs/eth-metrics/actions/workflows/release.yml/badge.svg)](https://github.com/bilinearlabs/eth-metrics/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/bilinearlabs/eth-metrics)](https://goreportcard.com/report/github.com/bilinearlabs/eth-metrics)
[![Tests](https://github.com/bilinearlabs/eth-metrics/actions/workflows/tests.yml/badge.svg)](https://github.com/bilinearlabs/eth-metrics/actions/workflows/tests.yml)
[![gitpoap badge](https://public-api.gitpoap.io/v1/repo/bilinearlabs/eth-metrics/badge)](https://www.gitpoap.io/gh/bilinearlabs/eth-metrics)

## Introduction

This tool monitors Ethereum consensus staking–pool performance. Given a set of labeled validators, it calculates:

* Rates of faulty head, source, and target votes (per the GASPER algorithm)
* Changes in rewards and penalties between consecutive epochs
* Proposed and missed blocks for each epoch

It leverages the beacon state, which makes it resource-intensive when tracking only a few validators but scales efficiently to monitor hundreds of thousands. All data is persisted in a SQLite database.

By default, metrics are computed from the latest head as the chain progresses in real time. You can backfill historical epochs using `--backfill-epochs` but note that this requires access to an archival node.

## Requirements

This project requires:
* An ethereum `consensus` client compliant with the http api running with `--enable-debug-rpc-endpoints`.
* An ethereum `execution` client compliant with the http api.

## Build

### Docker

Use the public docker image.

```console
docker pull bilinearlabs/eth-metrics:latest
```

Build with docker:

```console
git clone https://github.com/bilinearlabs/eth-metrics.git
docker build -t eth-metrics .
```

### Source

```console
git clone https://github.com/bilinearlabs/eth-metrics.git
go build
```

## Usage

The following flags are available:

```console
./eth-metrics --help
```

Place in `pool_a.txt` file the validators keys you want to track.

```
0xaddc693f9090db30a9aae27c047a95245f60313f574fb32729dd06341db55c743e64ba0709ee74181750b6da5f234b44
0xb6ba7d587c26ca22fd9b306c2f6708c3d998269a81e09aa1298db37ed3ca0a355c46054cb3d3dfd220461465b1bdf267
```

And in `pool_b.txt` other ones.
```
0xa59af0999c83f66de6cab8d833169fe10bce102d466c60c97c4e927210ac56e687c53feac8937c905cec5e87fccd72ce
0xb19b97fdf01ebd69ad69585e5c693f2ca251f16a315d65db0454e0632a1edc8ffdc21b24eabd26ba24a3a1228040fe8b
0x8f904676c4ca468ca9df4121bc7f7b1d969dfff93c8d2788b417dbe2e737aa1e644c31ebf36d933f8f1e5b6ebcfd6571
```

You can pass as many `--pool-name` as you want. The name of the pool will be taken from the file.
This example will monitor the performance of `pool_a` and `pool_b` and store in a SQLite database their performance, using said names as labels.

```console
./eth-metrics \
--eth1address=https://your-execution-endpoint \
--eth2address=https://your-consensus-endpoint \
--verbosity=debug \
--database-path=db.db \
--pool-name=pool_a.txt \
--pool-name=pool_b.txt \
```

Another option is to place in	`pools.csv` file the validators keys you want to track.

```csv
pool_a,0xaddc693f9090db30a9aae27c047a95245f60313f574fb32729dd06341db55c743e64ba0709ee74181750b6da5f234b44
pool_b,0xa59af0999c83f66de6cab8d833169fe10bce102d466c60c97c4e927210ac56e687c53feac8937c905cec5e87fccd72ce
```

And pass the `--validators-file` flag:

```console
./eth-metrics \
--eth1address=https://your-execution-endpoint \
--eth2address=https://your-consensus-endpoint \
--verbosity=debug \
--database-path=db.db \
--validators-file=keys.csv \
```

You can access the content of the database directly, or by using the API that allows to pass raw queries. For example, you can get the metrics from the latest epoch for `pool_a` as follows.

```
curl -X POST http://localhost:8080/query \
     -H "Content-Type: application/json" \
     -d "{\"sql\": \"SELECT * \
FROM t_pools_metrics_summary \
WHERE f_epoch = (SELECT MAX(f_epoch) FROM t_pools_metrics_summary) \
  AND f_pool = 'pool_a';\"}"
```

## Support

This project gratefully acknowledges the Ethereum Foundation for its support through their grant FY22-0795.
