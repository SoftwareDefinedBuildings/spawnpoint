#!/bin/sh
# This script is based on the Bosswave install script
# The Bosswave install script was in turn based on the docker install script
# It should be used with
#   'curl -sSL https://get.bw2.io/spawnd | sh'
# Or:
#   'wget -qO- https://get.bw2.io/spawnd | sh'

set -e
REL=0.5.0

command_exists() {
    command -v "$@" > /dev/null 2>&1
}

echo "Automated installer for Spawnd $REL"

if [ "$(uname -m)" != "x86_64" ]; then
    echo "Sorry, the Spawnd installer only supports x86_64 for now"
    exit 1
fi
if [ "$(uname -s)" != "Linux" ]; then
    echo "Sorry, the Spawnd installer only supports Linux for now"
    exit 1
fi
if [ ! -e /etc/issue ]; then
    echo "Sorry, the Spawnd installer only supports Ubuntu for now"
    exit 1
fi
if [ "$(cut -d' ' -f 1 /etc/issue)" != "Ubuntu" ]; then
    echo "Sorry, the Spawnd intaller only supports Ubuntu for now"
    exit 1
fi

if [ "$(pidof systemd)" = "" ] && [ "$(pidof systemd-udevd)" = "" ]; then
    echo "Unmet spawnd requirement: systemd"
    exit 1
fi

if ! command_exists docker; then
    echo "Unmet spawnd requirement: docker"
    exit 1
fi
docker ps > /dev/null 2>&1
if [ $? != 0 ]; then
    echo "Error: Docker appears to be installed but is not running"
    exit 1
fi

if ! command_exists bw2; then
    echo "Unmet spawnd requirement: bw2"
    exit 1
fi
if [ "$(pidof bw2)" = "" ]; then
    echo "Error: bw2 appears to be installed but is not running"
    exit 1
fi

sh_c = 'sh -c'
if [ "$user" != 'root' ]; then
    if command_exists sudo; then
        sh_c='sudo -E sh -c'
    elif command_exists su; then
        sh_c='su -c'
    else
        echo "Error: this installer needs the ability to run commands as root"
        echo "We are unable to find either sudo or su available to make this happen."
        exit 1
    fi
fi

echo "Pulling latest spawnd docker container"
$sh_c "docker pull jhkolb/spawnd:amd64"

$sh_c "mkdir /etc/spawnd"
$sh_c "systemctl stop spawnd"
$sh_c "$curl http://get.bw2.io/spawnd/0.x/Linux/x86_64/$REL/spawnd.service > /etc/systemd/system/spawnd.service"
$sh_c "systemctl daemon-reload"
$sh_c "sytemctl enable spawnd"
