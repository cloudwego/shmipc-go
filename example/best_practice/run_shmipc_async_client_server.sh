#!/bin/bash

cd shmipc_async_server
go build
cd ../shmipc_async_client
go build

cd ../shmipc_async_server
./shmipc_async_server &
SERVER_PID=$!
echo "server pid is $SERVER_PID"
sleep 1s

cd ../shmipc_async_client
./shmipc_async_client &
CLIENT_PID=$!
echo "client pid is $CLIENT_PID"

trap 'echo "exiting, now kill client and server";kill $CLIENT_PID;kill $SERVER_PID' SIGHUP SIGINT SIGQUIT SIGALRM SIGTERM
cd ../

sleep 1000s
