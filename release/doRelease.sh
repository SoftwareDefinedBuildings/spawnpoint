#! /bin/bash

pushd ../spawnd
go build
popd

export SPAWNPOINT_VERSION=`../spawnd/spawnd -v | cut -d ' ' -f 3`
mach_arch=`uname -m`
if [ "$mach_arch" == "x86_64" ]; then
    mach_arch="amd64"
fi
export ARCH=$mach_arch

echo "==Shipping release for Spawnpoint $SPAWNPOINT_VERSION=="
bash build.sh
python s3push.py
bash clean.sh
