#!/bin/sh
# This script is based on the Bosswave install script
# The Bosswave install script was in turn based on the Docker install script
# It should be used with
#   'curl -sSL https://get.bw2.io/spawnpoint | sh'
# Or:
#   'wget -qO- https://get.bw2.io/spawnpoint | sh'

set -e
REL="1.0.0-RC2"

command_exists() {
    command -v "$@" > /dev/null 2>&1
}

do_install() {

echo "Automated installer for Spawnd $REL"

sh_c='sh -c'
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

curl=''
if command_exists curl; then
    curl='curl -sSL'
elif command_exists wget; then
    curl='wget -qO-'
fi

if [ "$(uname -m)" != "x86_64" ]; then
    echo "Sorry, the Spawnd installer only supports x86_64 for now"
    exit 1
fi
if [ "$(uname -s)" != "Linux" ]; then
    echo "Sorry, the Spawnd installer only supports Linux for now"
    exit 1
fi
if [ ! -e /etc/issue ] || [ "$(cut -d' ' -f 1 /etc/issue)" != "Ubuntu" ]; then
    echo "Sorry, the Spawnd installer only supports Ubuntu for now"
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
$sh_c docker ps > /dev/null 2>&1
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

$sh_c "mkdir -p /etc/spawnd"
set +e
getent passwd spawnd > /dev/null
if [ $? -ne 0 ]; then
    ( set -x; $sh_c 'useradd -r -s /usr/sbin/nologin spawnd' )
fi

id -Gn spawnd | grep '\bdocker\b'
if [ $? -ne 0 ]; then
    ( set -x; $sh_c 'usermod -G docker spawnd' )
fi
set -e

$sh_c 'chown -R spawnd:spawnd /etc/spawnd'

echo "Pulling latest spawnd docker container"
$sh_c "docker pull jhkolb/spawnd:amd64"

set +e
$sh_c "systemctl stop spawnd"
set -e
$sh_c "$curl http://get.bw2.io/spawnd/1.x/Linux/x86_64/$REL/spawnd.service > /etc/systemd/system/spawnd.service"
dockerClientVersion="$(docker version -f {{.Client.APIVersion}})"
$sh_c "sed -i 's/{{dockerClientVersion}}/$dockerClientVersion/' /etc/systemd/system/spawnd.service"
$sh_c "systemctl daemon-reload"
$sh_c "systemctl enable spawnd"

if [ ! -e /etc/spawnd/config.yml ]; then
    $sh_c "cat >/etc/spawnd/config.yml" <<-'EOF'
	bw2Entity: {{entity}}
	path: {{path}}
	alias: {{alias}}
	memory: {{memory}}
	cpuShares: {{cpuShares}}
	bw2Agent: 172.17.0.1:28589
	enableHostNetworking: false
	enableDeviceMapping: false
	EOF

    entity=''
    path=''
    memory=''
    cpuShares=''
    if [ -n "$SPAWND_INSTALLER_ENTITY" ]; then
        entity="$SPAWND_INSTALLER_ENTITY"
    else
        entity="$(whiptail --nocancel --inputbox "Type the path to the Bosswave Entity for this Spawnpoint." \
                  10 78 --title "Entity Selection" 3>&1 1>&2 2>&3)"
    fi
    $sh_c "cp $entity /etc/spawnd/"

    if [ -n "$SPAWND_INSTALLER_PATH" ]; then
        path="$SPAWND_INSTALLER_PATH"
    else
        path="$(whiptail --nocancel --inputbox "Base URI for this Spawnpoint to advertise on:" \
                10 78 --title "Base URI Selection" 3>&1 1>&2 2>&3)"
    fi
    sdAlias="$(echo "$path" | awk -F'/' '{print $NF}')"

    if [ -n "$SPAWND_INSTALLER_MEMORY" ]; then
        memory="$SPAWND_INSTALLER_MEMORY"
    else
        memory="$(whiptail --nocancel --inputbox "Memory allocation for this Spawnpoint, in MiB (e.g. 2048):" \
                    10 78 --title "Memory Allocation" 3>&1 1>&2 2>&3)"
    fi

    if [ -n "$SPAWND_INSTALLER_CPU_SHARES" ]; then
        cpuShares="$SPAWND_INSTALLER_CPU_SHARES"
    else
        cpuShares="$(whiptail --nocancel --inputbox "CPU Shares for this Spawnpoint (Enter 1024 per Core)" \
                     10 78 --title "CPU Shares" 3>&1 1>&2 2>&3)"
    fi

    entityFile="$(echo "$entity" | awk -F'/' '{print $NF}')"

    # / in sed subsitution commands is so mainstream!
    $sh_c "sed -i 's#{{entity}}#/etc/spawnd/$entityFile#' /etc/spawnd/config.yml"
    $sh_c "sed -i 's#{{path}}#$path#' /etc/spawnd/config.yml"
    $sh_c "sed -i 's#{{alias}}#$sdAlias#' /etc/spawnd/config.yml"
    $sh_c "sed -i 's#{{memory}}#$memory#' /etc/spawnd/config.yml"
    $sh_c "sed -i 's#{{cpuShares}}#$cpuShares#' /etc/spawnd/config.yml"
    $sh_c "chown spawnd:spawnd /etc/spawnd/config.yml"

    $sh_c "chown spawnd:spawnd /etc/spawnd/$entityFile"
    $sh_c "chmod 0400 /etc/spawnd/$entityFile"

fi

$sh_c "systemctl start spawnd"
}

do_install
