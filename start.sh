#!/bin/bash
go build ./cmd/tqdbproxy/
./tqdbproxy
rm tqdbproxy
