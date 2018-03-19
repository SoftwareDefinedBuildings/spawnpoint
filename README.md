# Spawnpoint

Spawnpoint is a platform to deploy managed containers across distributed
infrastructure while using [Bosswave](https://github.com/immesys/bw2) for access
control and authentication. It is primarily intended for containerized services
that communicate over Bosswave.

A "Spawnpoint" is a host, e.g., a cloud-based virtual machine or on-premises
server, that supports service execution by providing storage, Bosswave
infrastructure, and a pool of memory and compute resources. Individual services
are allocated slices of the host's resources, and these allocations are enforced
by a daemon process running on that host. The daemon also manages the lifecycle
of service containers, automatically restarting them when they fail, etc.

The Spawnpoint host daemon performs all of its communication via Bosswave,
meaning it only accepts commands to deploy new services, or to manipulate an
existing service, from principals that are authorized to issue those commands
as messages on the associated Bosswave URIs (topics). The daemon also publishes
Bosswave messages to advertise available resources on the host.

## Components
* `spawnd` is the Spawnpoint host management daemon. Each Spawnpoint
  instance is backed by a running daemon that provides a Bosswave interface for
  the deployment and control of services and manages service resource
  consumption.
* `spawnctl` is a command line tool for interaction with Spawnpoint daemons. It
  can be used to discover available Spawnpoints, to deploy a new service, or to
  restart and stop existing services.
* `spawnclient` is a library that enables interaction with Spawnpoint daemons
  from Go programs. It provides the same set of capabilities as `spawnctl`.
* `spawnable` provides utilities for easy development of Spawnpoint services in
  Go.

## Writing a Spawnpoint Service
See the [wiki](https://github.com/SoftwareDefinedBuildings/spawnpoint/wiki/Writing-a-Spawnpoint-Service-in-Go)
for a step-by-step walkthrough on writing a Spawnpoint service in Go, which is
the recommended implementation language.

## Interacting with Spawnpoints Using `spawnctl`
The `spawnctl` command line utility is the simplest way to communicate with
hosts that make themselves available for service execution by running the
Spawnpoint daemon (`spawnd`).

`spawnctl` will automatically respect the value specified in the
`BW2_DEFAULT_ENTITY` environment variable as the Bosswave entity to use for all
Bosswave operations. However, this can be overridden using the `-e` flag.

Similarly, `spawnctl` will automatically attempt to establish a session with the
Bosswave agent at the network address defined in the `BW2_AGENT` environment
variable, but this can be overridden using the `-r` flag.

### Searching for Spawnpoints
Use the `scan` command to discover all Spawnpoints that are communicating over
Bosswave URIs that begin with a common base URI. For example:

```
$ spawnctl scan -u scratch.ns
Discovered 2 Spawnpoint(s)
[beta] seen 17 Mar 18 17:42 PDT (830ms) ago at scratch.ns/spawnpoint/beta
Available CPU Shares: 1536/2048
Available Memory: 1536/2048
1 Running Service(s)
  • demosvc
[alpha] seen 17 Mar 18 16:53 PDT (49m9.44s) ago at scratch.ns/spawnpoint/alpha
Available CPU Shares: 2048/2048
Available Memory: 2048/2048
0 Running Service(s)
```

If your scan only finds one Spawnpoint, more detailed information is produced:
```
$ spawnctl scan -u oski
[beta] seen 17 Mar 18 17:44 PDT (20.2s) ago at oski/spawnpoint/beta
Available CPU Shares: 1536/2048
Available Memory: 1536/2048
1 Running Service(s)
• [demosvc] seen 17 Mar 18 17:44 PDT (18.58s) ago.
  CPU: ~1.02/512 Shares. Memory: 3.86/512 MiB
```

### Deploying a Service
Use `spawnctl`'s `deploy` command to issue a command to a Spawnpoint host that
instructs it to provision and start a new container for your service. You must
specify the Bosswave URI for the Spawnpoint and a YAML configuration file that
describes the service and its requirements.

Each service is identified by a unique, human-readable name. This is specified
either in the configuration file or directly on the command line by the `-n`
flag.

To run a service named `demosvc` with a configuration specified in the file
`deploy.yml` (more on configurations below), on a Spawnpoint operating at
the base Bosswave URI `scratch.ns/spawnpoint/alpha`:

```
$ spawnctl deploy -u scratch.ns/spawnpoint/alpha -c deploy.yml -n demosvc
Tailing service logs. Press CTRL-c to exit...
<snip>
[SUCCESS] Service container has started
```

After the deployment command has been issued, `spawnctl` will tail the output
of the new container, displaying any messages written by the container process
to its local `stdout` or `stderr`. You must press `CTRL-c` to stop tailing.

Alternatively, most `spawnctl` operations support a timeout, specified by the
`-t` flag on the command line. If we add `-t 5s` to the preceding command,
log tailing will cease after 5 seconds.

If not otherwise specified, service containers use `jhkolb/spawnable` as their
base Docker image. The working directory for Spawnpoint containers is
`/srv/spawnpoint`.

#### Writing a Service Configuration
Each service that runs on a Spawnpoint has its configuration specified in a YAML
file containing a sequence of key-value parameters. The following parameters are
required for any service:
* `cpuShares`: The number of CPU shares to reserve for this service. 1024 shares
  equals one core of the host machine.
* `memory`: The amount of memory, in MiB, to reserve for this service.
* `run`: The entrypoint command for the service container, e.g. running a script
  or invoking an executable file.

The following configuration parameters are optional, but allow a service to make
use of other important Spawnpoint features.
* `bw2Entity`: The Bosswave entity file that is used to identify the service. This
  is injected into the container as the file `/srv/spawnpoint/entity.key`.
  Within the container, the `BW2_DEFAULT_ENTITY` environment variable is set to
  this path.
* `image`: An alternative Docker image to use as the base for a service
  container. Any publicly available Docker image (e.g., posted on Docker's hub)
  is acceptable.
* `source`: A GitHub URL (must be HTTPS) pointing to a repository to be cloned
  before starting the container.
* `build`: A sequence of commands to run after checking out the necessary
  source code, e.g., for documentation.
* `autoRestart`: A boolean specifying if the service's container should be
  automatically restarted upon termination. Defaults to `false`.
* `includedFiles`: A list of files on the deploying host that should be included
  in the container. All files are copied directly into the Spawnpoint
  container's working directory (`/srv/spawnpoint`) but retain their original
  names.
* `includedDirectories`: A list of directories on the deploying host to include
  in the Spawnpoint container. All directories retain their names, but are
  placed under `/srv/spawnpoint` within the container.
* `volumes`: A list of volume names to be used by the container. If a volume
  does not exist on the hosting Spawnpoint, it will be created. Otherwise, the
  existing volume is attached to the container. All volumes are mounted under
  the `/srv` directory, so a volume named `foo` is available within the
  container as `/srv/foo`.
* `useHostNet`: If the hosting Spawnpoint daemon allows it, service containers
  may directly use the host machine's network stack rather than Docker's bridge
  interface. _Note that this presents a security risk_.
* `devices`: A list of device file paths to map from the host machine into the
  Spawnpoint container. This functionality must be specifically enabled by the
  host daemon, because it _represents a security risk_.

### Conveniently Re-running a Deployment
You can use the `deploy-last` command to rerun the same `deploy` command that
was last executed in the current directory. For example, if you run this command
immediately after the `deploy` command given above:

```
$ spawnctl deploy-last
This will run:
    spawnctl deploy -e <BW2 Entity> -u scratch.ns/spawnpoint.alpha -c deploy.yml -n demosvc
Proceed? [Y/n]
```
To confirm execution of the command, type in `Y` or `y` and hit `<ENTER>`. To
skip this confirmation phase, pass the flag `-y` to `deploy-last`.

Spawnpoint stores previous deploy commands in a history file, specified by the
`SPAWNCTL_HISTORY_FILE` environment variable. If this variable is not set, the
history file defaults to `~/.spawnpoint_history`.

### Manipulating a Running Service
To restart or stop a running service, you must know the base URI of its host
Spawnpoint instance as well as its unique name. To restart the service
`demoservice` on Spawnpoint `scratch.ns/spawnpoint/alpha`:
```
$ spawnctl restart -u scratch.ns/spawnpoint.alpha -n demosvc
Tailing service logs. Press CTRL-c to eit...
[SUCCESS] Restarted service container
<snip>
```

To stop rather than restart the service, simply replace `restart` with `stop` as
the command for `spawnctl`. Just as with deployment, the service's logs are
tailed until the user exits via `<CTRL>-c` or a timeout specified by `-t`
expires.

To replace an existing service with a new version, you must first stop the
original service explicitly by using `spawnctl stop`. Then, run a `spawnctl
deploy` with the new version on the same Spawnpoint.

## Running a Spawnpoint Daemon
To enable Spawnpoint services to run on a machine, you will need to take the
following preliminary steps:
1. Install the latest release of Bosswave 2
2. Install Docker

We have provided a Spawnpoint installation shell script. The easiest way to use
it is to run the following command:
```
$ curl get.bw2.io/spawnpoint | bash
```

The script will download the `spawnd` docker container and initialize all of the
necessary configuration files for you. See the installation script
documentation, under the `installer` directory in the Spawnpoint repository, for
full details.

Just like Spawnpoint services, the Spawnpoint daemon is configured using a YAML
file of key-value parameters. The required parameters are:
* `bw2Entity`: The Bosswave entity that identifies this Spawnpoint and its
  permissions within the Bosswave URI space.
* `path`: The Spawnpoint's base Bosswave URI. It should uniquely identify this
  Spawnpoint daemon instance. The last element of this base path will be used as
  a shorthand identifier for this host.
* `cpuShares`: The number of CPU shares in the daemon's global pool. 1024 shares
  correspond to a single CPU core.
* `memory`: The size of the daemon's global memory pool, in MiB.

The optional parameters, which only need to be specified to override default
values, are:
* `bw2Agent`: The Bosswave agent that should be used by the daemon for all
  pub/sub operations. Defaults to `127.0.0.1:28589`.
* `enableHostNetworking`: Allow containers to use the host's networking stack.
  Defaults to `false`. _Enabling host networking represents a security risk_.
* `enableDeviceMapping`: Allow devices from the host's file system to be mapped
  into service containers. Defaults to `false`. _Enabling device mapping represents
  a security risk_.
