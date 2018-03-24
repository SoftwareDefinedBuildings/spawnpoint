#! /bin/bash

# Rebuild spawnd container and push to Docker hub
pushd ../spawnd
go build
popd
pushd ../containers/spawnd
cp ../../spawnd/spawnd .
docker build --pull --no-cache -t jhkolb/spawnd:$SPAWNPOINT_VERSION .
docker push jhkolb/spawnd:$SPAWNPOINT_VERSION
popd

# Rebuild spawnable container
pushd ../containers/spawnable
docker build --pull --no-cache -t jhkolb/spawnable:amd64 .
docker push jhkolb/spawnable:amd64
popd

# Compile spawnctl binary
pushd ../spawnctl
go build
popd

# Prepare installer script
cp ../installer/installer.sh .
sed -i "s/{{release}}/$SPAWNPOINT_VERSION/" installer.sh

mv ../spawnd/spawnd .
mv ../spawnctl/spawnctl .
cp ../containers/spawnd/spawnd.service .
