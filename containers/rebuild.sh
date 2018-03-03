#!/usr/bin/env bash

# Spawnd
cp ../spawnd/spawnd spawnd/
pushd spawnd/
docker build --pull --no-cache -t jhkolb/spawnd:amd64 .
docker push jhkolb/spawnd:amd64
popd

# Spawnable
pushd spawnable/
docker build --pull --no-cache -t jhkolb/spawnable:amd64 .
docker push jhkolb/spawnable:amd64
popd

