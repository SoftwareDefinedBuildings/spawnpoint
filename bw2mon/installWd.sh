#! /bin/bash

# This script requires the 'sdmon' binary to be installed and on your path
# The easiest way to set this up: 'go get github.com/immesys/wd/sdmon'

# You must also specify the watchdog prefix as a command line argument
# Example: installWd 410
if [ $# -lt 1 ]; then
    echo "Usage: $0 <prefix>"
    exit 1
fi

sdmon \
    --prefix $1
    --interval 5m
    --holdoff 10m
    --unit spawnd
