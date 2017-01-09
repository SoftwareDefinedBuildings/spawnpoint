#!/usr/bin/env bash

${REPO_PREFIX:=jhkolb}

# Spawnable amd64
pushd spawnable/amd64
docker build --pull --no-cache -t $REPO_PREFIX/spawnpoint:amd64 .
docker push $REPO_PREFIX/spawnpoint:amd64
popd

# Spawnable armv7
pushd spawnable
rsync -PHav --delete armv7/ ubuntu@10.4.10.200:~/spawnpointbuild
ssh ubuntu@10.4.10.200 "cd spawnpointbuild && docker build --pull --no-cache -t $REPO_PREFIX/spawnpoint:armv7 . && docker push $REPO_PREFIX/spawnpoint:armv7"
popd

# Python Spawnable amd64
pushd spawnable-py/amd64
docker build --pull --no-cache -t $REPO_PREFIX/spawnable-py:amd64 .
docker push $REPO_PREFIX/spawnable-py:amd64
popd

# Spawnd amd64
cp ../spawnd/spawnd spawnd/amd64/
pushd spawnd/amd64
docker build --pull --no-cache -t $REPO_PREFIX/spawnd:amd64 .
docker push $REPO_PREFIX/spawnd:amd64
popd

# Spawnd armv7
pushd spawnd
rsync -PHav --delete ../../ ubuntu@10.4.10.200:~/spawndbuild
ssh ubuntu@10.4.10.200 "cd spawndbuild/spawnd && TMDIR=/home/ubuntu/tmp go get -d ./... && TMPDIR=/home/ubuntu/tmp go build . && cp spawnd ../containers/spawnd/armv7 && cd ../containers/spawnd/armv7 && docker build --pull --no-cache -t $REPO_PREFIX/spawnd:armv7 . && docker push $REPO_PREFIX/spawnd:armv7"
popd
