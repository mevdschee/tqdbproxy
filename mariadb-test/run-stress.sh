#!/bin/bash

# MariaDB Proxy Stress Test Runner
# This script executes the official MariaDB main suite against the TQDBProxy.

STRESS_DIR=$(dirname $(readlink -f $0))
TMP_DIR="/tmp/mariadb-stress-$(date +%s)"
LOG_DIR="$STRESS_DIR/logs"

# Configuration
SUITE_BASEDIR="$STRESS_DIR/pkg_suite"
TESTS_FILE="$STRESS_DIR/main-tests.txt"
THREADS=10
TEST_COUNT=500

# Ensure SUITE_BASEDIR (symlinks) exists
if [ ! -d "$SUITE_BASEDIR" ]; then
    echo "Initializing pkg_suite symlinks..."
    mkdir -p "$SUITE_BASEDIR/t"
    mkdir -p "$SUITE_BASEDIR/r"
    ln -sf /usr/share/mysql/mysql-test/main/*.test "$SUITE_BASEDIR/t/"
    ln -sf /usr/share/mysql/mysql-test/main/*.result "$SUITE_BASEDIR/r/"
fi

# Ensure TESTS_FILE exists (generated from system main suite)
if [ ! -f "$TESTS_FILE" ]; then
    echo "Generating test list from system package..."
    ls -F /usr/share/mysql/mysql-test/main/*.test | sed 's|.*/||; s/\.test$//' > "$TESTS_FILE"
fi

# Find stress test script
STRESS_PL=$(find /usr/share/mysql/mysql-test -name "mariadb-stress-test.pl" | head -n 1)
if [ -z "$STRESS_PL" ]; then
    echo "Error: mariadb-stress-test.pl not found. Please install mariadb-test package."
    exit 1
fi

MYSQLTEST=$(which mysqltest)
if [ -z "$MYSQLTEST" ]; then
    echo "Error: mysqltest binary not found. Please install mariadb-test package."
    exit 1
fi

mkdir -p "$TMP_DIR"
mkdir -p "$LOG_DIR"

echo "Running MariaDB main stress test ($THREADS threads, $TEST_COUNT executions)..."

perl "$STRESS_PL" \
  --stress-suite-basedir="$SUITE_BASEDIR" \
  --stress-basedir="$TMP_DIR" \
  --server-logs-dir="$LOG_DIR" \
  --server-host=127.0.0.1 \
  --server-port=3307 \
  --server-user=tqdbproxy \
  --server-password=tqdbproxy \
  --server-database=tqdbproxy \
  --mysqltest="$MYSQLTEST" \
  --threads="$THREADS" \
  --test-count="$TEST_COUNT" \
  --stress-tests-file="$TESTS_FILE" \
  --suite="main" \
  --abort-on-error=0 \
  --cleanup

RET=$?

# Cleanup tmp dir
rm -rf "$TMP_DIR"

if [ $RET -eq 0 ]; then
    echo "Stress test PASSED successfully."
else
    echo "Stress test FAILED. Check logs in $LOG_DIR"
fi

exit $RET
