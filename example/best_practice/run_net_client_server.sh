#!/bin/bash

cd net_server
go build
cd ../net_client
go build

cd ../net_server
./net_server &
SERVER_PID=$!
echo "server pid is $SERVER_PID"
sleep 1s

cd ../net_client
./net_client &
CLIENT_PID=$!
echo "client pid is $CLIENT_PID"

trap 'echo "exiting, now kill client and server";kill $CLIENT_PID;kill $SERVER_PID' SIGHUP SIGINT SIGQUIT SIGALRM SIGTERM
cd ../

sleep 1000s
