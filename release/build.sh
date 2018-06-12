#! /bin/bash

# Rebuild spawnd container and push to Docker hub
pushd ../spawnd
go build
popd
pushd ../containers/spawnd
cp ../../spawnd/spawnd .
docker build --pull --no-cache -t jhkolb/spawnd:$SPAWNPOINT_VERSION"-"$ARCH .
docker push jhkolb/spawnd:$SPAWNPOINT_VERSION"-"$ARCH
popd

# Rebuild spawnable container
pushd ../containers/spawnable
docker build --pull --no-cache -t jhkolb/spawnable:$ARCH .
docker push jhkolb/spawnable:$ARCH
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
