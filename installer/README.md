# Spawnd Automated Installer

To install or update `spawnd` on your machine, you can use our script. Much like
Bosswave, installation and updating are done like so:
```
curl get.bw2.io/spawnpoint | bash
```

Currently, the script only supports 64-bit Ubuntu Linux installations that use
`systemd` (i.e. versions 16.04 and later). The script should fail gracefully
when run on other platforms.

Additionally, the installer currently does not configure the metadata that is
advertised by the spawnpoint. To do this, you must edit `/etc/spawnd/metadat.yml`
manually.

This script will then take the following steps:
  1. Check for the necessary dependencies: `bw2` and `docker`
  2. Create a `spawnd` user, if one does not already exist, and add it to the
     `docker` group.
  3. Pull the latest Spawnpoint daemon container, `jhkolb/spawnd:amd64` from
     Docker's repository.
  4. Install a `systemd` unit file to manage execution of the `spawnd` container.
  5. Automatically populates the configuration file in `/etc/spawnd/config.yml`
     By default, this is done through interactive prompts, but each of these
     parameters may also be defined through environment variables if a
     non-interactive installation is required.
     * `entity` (`$SPAWND_INSTALLER_ENTITY`): The absolute path to the Bosswave
       entity file that will identify this Spawnpoint for all Bosswave operations.
       Note that this file will be copied into `/etc/spawnd/`.
     * `path` (`$SPAWND_INSTALLER_PATH`): The base Bosswave URI for the Spawnpoint.
     * `memAlloc` (`$SPAWND_INSTALLER_MEM_ALLOC`): The total amount of memory to
       allocate for the containers running on this Spawnpoint. Remember, this is
       either in MiB by default or in GiB with a "G" suffix on the string.
     * `cpuShares` (`$SPAWND_INSTALLER_CPU_SHARES`): The total amount of CPU
       shares to allocate to containers running on this Spawnopint. Remember,
       this should be 1024 shares per core.
  6. Create an empty placeholder for `/etc/spawnd/metadata.yml` if the file does
     not exist already.
  7. Start up the new `spawnd` service, after enabling it to start on boot as
     well.

### What about host networking?
In general, allowing containers to use the host network is discouraged unless
you know what you are doing. Therefore, it is disabled by the installer by
default, but the installer will not change your old settings when performing
an update.
