#! /usr/bin/env bash

export SHMIPC_LOG_LEVEL=0
export SHMIPC_DEBUG_MODE=1

## hot restart
export IS_HOT_RESTART=1
export HOT_RESTART_EPOCH=1122
export DEBUG_PORT=20002
./server

