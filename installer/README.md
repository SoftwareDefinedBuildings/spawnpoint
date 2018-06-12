# Spawnd Automated Installer

To install or update `spawnd` on your machine, you can use our script. Much like
Bosswave, installation and updating are done like so:
```
curl get.bw2.io/spawnpoint | bash
```

Currently, the script only supports `amd64` and `armv7l` versions of Linux that
use `systemd` (e.g., Ubuntu 16.04 and later). The script should fail gracefully
when run on other platforms.

This script will then take the following steps:
  1. Check for the necessary dependencies: either `bw2` or `ragent` as well as
     `docker`
  2. Create a `spawnd` user, if one does not already exist, and add it to the
     `docker` group.
  3. Pull the appropriate Spawnpoint daemon container from Docker's repository.
  4. Install a `systemd` unit file to manage execution of the `spawnd` container.
  5. Automatically populate the configuration file in `/etc/spawnd/config.yml`
     By default, this is done through interactive prompts, but each of these
     parameters may also be defined through environment variables if a
     non-interactive installation is required.
     * `entity` (`$SPAWND_INSTALLER_ENTITY`): The absolute path to the Bosswave
       entity file that will identify this Spawnpoint for all Bosswave operations.
       Note that this file will be copied into `/etc/spawnd/`.
     * `path` (`$SPAWND_INSTALLER_PATH`): The base Bosswave URI for the Spawnpoint.
     * `memory` (`$SPAWND_INSTALLER_MEMORY`): The total amount of memory to
       allocate for the containers running on this Spawnpoint.
     * `cpuShares` (`$SPAWND_INSTALLER_CPU_SHARES`): The total amount of CPU
       shares to allocate to containers running on this Spawnopint. Remember,
       this should be 1024 shares per core.
  7. Start up the new `spawnd` service, after enabling it to start on boot as
     well.
