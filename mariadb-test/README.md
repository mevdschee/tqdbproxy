# MariaDB Proxy Stress Testing

This directory contains a suite of stress tests designed to verify the stability of the TQDBProxy under high-concurrency MariaDB binary protocol workloads.

It uses the **official MariaDB main suite** (extended with custom prepared statement tests).

## Prerequisites

You must have the MariaDB test framework installed:

```bash
sudo apt-get install mariadb-test
```

## Usage

Ensure the `tqdbproxy` is running and listening on port 3307. Then execute the runner script:

```bash
./run-stress.sh
```

## Configuration

The `run-stress.sh` script is configured to:
- use **10 concurrent threads**.
- perform **500 total test executions**.
- pick tests randomly from the MariaDB `main` suite

## Output

- **Console**: Displays progress and final pass/fail status.
- **`logs/`**: Contains detailed execution and rejection logs if failures occur.
