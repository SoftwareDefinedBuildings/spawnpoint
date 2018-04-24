## Overview
These Python scripts have been provided to assist users migrating from early
versions of Spawnpoint (0.x) to the latest version (1.x).

## Updating a Daemon Configuration File
The script `convertDaemonConfig.py` is used to convert a `spawnd` 0.x
configuration file into a new `spawnd` configuration that is compatible with
the 1.x releases of the system. It is used like so:
```
$ python convertDaemonConfig.py oldConfig.yml newConfig.yml
```

Here, `oldConfig.yml` is the name of the current configuration file, while
`newConfig.yml` is the name of the new 1.x-compatible file that will be created.

Most of the features of the 0.x daemon configuration files have been preserved
in the 1.x release. The exceptions are:
  1. The `containerRouter` field has been deprecated. Now, both the daemon and
     the containers it managers talk to the same Bosswave endpoint, which has
     been renamed to `bw2Agent` in configuration files for clarity.
  2. You may no longer specify an `alias` for the `spawnd` instance. Instead,
     this is assumed to be the last element of the daemon's base URI.

## Updating a Service Configuration File
This is very similar to updating a daemon configuration. The script
`convertServiceConfig.py` is used like so:
```
$ python convertServiceConfig.py oldDeploy.yml newDeploy.yml
```

`oldDeploy.yml` is the 0.x-compatible service configuration file, and
`newDeploy.yml` is the name of the 1.x-compatible file that will be created.

Most of the features of the 0.x service configuration files have been preserved.
The exceptions are:
  1. The `overlayNet` field (and Docker overlay networking in general) has been
     deprecated.
  2. The `restartInt` field has been deprecated. `spawnd` now reserves the right
     to handle service restart intervals internally.
