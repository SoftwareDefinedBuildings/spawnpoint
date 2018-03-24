#! /bin/bash

if [ $# -lt 1 ]; then
    echo "Usage: $0 <release_version>"
    exit 1
fi

export SPAWNPOINT_VERSION=$1
bash build.sh
python s3push.py
bash clean.sh
