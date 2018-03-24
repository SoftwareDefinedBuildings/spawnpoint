#! /bin/bash

pushd ../spawnd
go build
popd

export SPAWNPOINT_VERSION=`../spawnd/spawnd -v | cut -d ' ' -f 3`
echo "==Shipping release for Spawnpoint $SPAWNPOINT_VERSION=="
bash build.sh
python s3push.py
bash clean.sh
