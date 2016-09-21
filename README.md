# Spawnpoint

Spawnpoint is a tool to deploy, run, and monitor services. It is primarily
intended for services that communicate over
[BOSSWAVE](https://github.com/immesys/bw2), but it also has features for
applications that wish to make use of native means of communication. A
"Spawnpoint" is an individual machine that supports service execution by
providing storage, a BOSSWAVE router for communication, and a pool of memory and
compute resources. Individual services run in dedicated Docker containers that
are instantiated and managed by a Spawnpoint.

## Components
* `spawnd` is the Spawnpoint daemon process. Each Spawnpoint is backed by a
  running daemon to accept new services for execution, manage the resource
  consumption of services, and provide a BOSSWAVE interface for the control and
  monitoring of running services.

* `spawnctl` is a command line tool for interaction with local or remote
  Spawnpoint daemons. It can be used to discover available Spawnpoints, deploy a
  new service to a Spawnpoint, or to stop or restart a service that is already
  running.

* `spawnclient` enables interaction with Spawnpoint daemons from within Go
  programs. It provides the same set of capabilities as `spawnctl`.

* `spawnable` provides utilities for easy development of services that fit into
  the Spawnpoint/BOSSWAVE ecosystem. These utilities help with parsing parameter
  files (more on this below), manipulating metadata, and interacting with
  BOSSWAVE.

## Running Your Own Spawnpoint
To allow BOSSWAVE services to run on your own machine, you will need to:

1. Install Docker. Spawnpoint currently supports Docker version 1.11.
2. Optionally, set up or connect to an `etcd` instance
3. Configure a local Spawnpoint
4. Run a `spawnd` process on your machine

### Installing Docker
Follow the [instructions](https://docs.docker.com/engine/installation) provided
by the Docker team for your platform.

### Setting up `etcd`
If you wish to allow containers running on your machine to make use of a VLAN
overlay network for native communications, the Docker daemon must connect to
a key/value store. We recommend `etcd` and its associated
[Docker container](https://quay.io/repository/coreos/etcd). Documentation is
available [here](https://coreos.com/etcd/docs/latest/docker_guide.html).

Once `etcd` is running, you will also need to pass configuration flags to the
docker Daemon when it is started. The steps to achieve this vary by platform. On
Ubuntu, for example, you will need to edit the `docker.service` file used by
`systemd`. The two required configuration flags are:

1. `cluster-store`: The address and port of the key/value to use for overlay
   networking, e.g. `etcd://foo.bar:4001`
2. `cluster-advertise`: An address and port that can be used to reach the Docker
   daemon running on this machine. You may use an interface or IP address,
   e.g. `eth0:2375`.

### Configuring a Spawnpoint
To configure a Spawnpoint daemon, create a simple YAML file. This file will
contain a collection of key/value pairs that serve as parameters for Spawnpoint.
Currently, the valid parameters are:

* `entity` (required): A file containing the entity that will serve as the Spawnpoint's
  identity to BOSSWAVE. It is used by the Spawnpoint daemon to publish and
  subscribe to BOSSWAVE URIs.

* `alias` (required): A short and human readable name for the Spawnpoint.

* `path` (required): The Spawnpoint's base URI. It should uniquely identify the
  Spawnpoint and is used to form the BOSSWAVE URIs for the Spawnpoint's control
  and logging.

* `localRouter` (optional): An address and port for the Spawnpoint's designated BOSSWAVE
  router. Defaults to `127.0.0.1:28589`.

* `containerRouter` (optional): An address and port at which Docker containers
  running on this machine can reach a BOSSWAVE router. By default, containers
  will attempt to connect at `127.0.0.1:28589`, but this is unlikely to work as
  most containers are behind a Docker bridge network.

* `memAlloc` (required): The total amount of memory to be made available to
  spawned services. This quantity should be expressed in either MB, signified
  by a suffix of "M" or "m" (e.g. "512M)" or in GB, signified by a suffix of
  "G" or "g" (e.g. "4G").

* `cpuShares` (required): The total number of CPU shares to be used by spawned
  services. This should be 1024 shares per core.

A typical Spawnpoint configuration file might look as follows. Note that the
`localRouter` parameter is omitted, and thus it takes on the default value.
```yaml
entity: ~/bosswave/spawnpointTest.key
path: scratch.ns/spawnpoint/alpha
alias: alpha
memAlloc: 4G
cpuShares: 4096
containerRouter: 172.17.0.1:28589
```
### Metadata
A Spawnpoint daemon can be configured to advertise its own metadata as
attribute/value pairs. Simply create another YAML file of key/value pairs. All
of these pairs can then be advertised as metadata over BOSSWAVE. Use the `-m`
command flag (described below) when running `spawnd`.

### Running the Spawnpoint Daemon Natively
Use `go build` to compile the Spawnpoint daemon binary from its Go source code.
You can then run `./spawnd run -c spawnConfig.yml` to run a Spawnpoint according
to the configuration given in the file `spawnConfig.yml`. If no `-c` flag is
used, Spawnpoint will by default look for a file named `config.yml` in the
current directory.

Use the `-m` flag to specify a metadata file, e.g. entering `./spawnd run -m
metadata.yml` will cause the daemon to advertise all metadata key/value pairs
contained in the file `metadata.yml`. If this flag is omitted, no metadata is
advertised.

### Running the Spawnpoint Daemon as a Container
We have provided an image, available from the Docker hub at `jhkolb/spawnd`,
that can be used to run the Spawnpoint daemon as a Docker container. The
`systemd` unit file contained in this GitHub repo provides an example of using
the container. In particular, you will need to map two volumes for the
container:

1. `/etc/spawnd`: This is where the container will look for YAML configuration
   and metadata files.
2. `/var/run/docker.sock`: This gives the Spawnpoint container access to the
   host's Docker daemon. Unless you have an unusual Docker configuration, this
   volume will map between identical locations in the host and container.

## Interacting With Spawnpoints Using `spawnctl`
The best way to make use of running Spawnpoints is through the `spawnctl`
command line utility. Like the `bw2` command line tool, `spawnctl` will
automatically use the entity specified in the `BW2_DEFAULT_ENTITY` environment
variable for all BOSSWAVE operations, but you may override this with the `-e`
flag.

### Looking for Spawnpoints
Use the `scan` command to discover all Spawnpoints with paths that begin with a
common base URI. For example:
```
$ spawnctl scan -u scratch.ns/spawnpoint
<snip> [Info] Connected to BOSSWAVE router version 2.4.11 - 'Hadron'
Discovered 2 SpawnPoints:
[beta] seen 24 May 16 23:39 PDT (189h30m36.428869615s) ago at <snip>/spawnpoint/beta
    Available Memory: 16384 MB, Available Cpu Shares: 8192
[alpha] seen 01 Jun 16 12:19 PDT (8h50m34.559646724s) ago at <snip>/spawnpoint/alpha
    Available Memory: 4096 MB, Available Cpu Shares: 4096
```

If your scan only matches one Spawnpoint, more detailed information, such as the
currently running services and the Spawnpoint's metadata, is printed out.
```
$ spawnctl scan -u scratch.ns/spawnpoint/alpha
<snip> [Info] Connected to BOSSWAVE router version 2.4.11 - 'Hadron'
Discovered 1 SpawnPoint(s):
[alpha] seen 07 Sep 16 21:23 PDT (4.470620813s) ago at <snip>/spawnpoint/alpha
    Available Memory: 1536 MB, Available Cpu Shares: 1024
• [demosvc] seen 07 Sep 16 21:23 PDT (2.247837383s) ago
    Memory: 512 MB, Cpu Shares: 1024
Metadata:
  • arch: amd64
```

### Deploying a Service
Use the `deploy` command to install a new service on a particular Spawnpoint.
You must specify the Bosswave URI for the Spawnpoint, a configuration file that
is used to initialize the service, and a name for the new service. For example,
to run a service named `demoservice`, with a configuration specified in the file
`deploy.yml` (more on this below), on `scratch.ns/spawnpoint/alpha`:
```
$ spawnctl deploy -u scratch.ns/spawnpoint/alpha -c deploy.yml -n demosvc
<snip> [Info] Connected to BOSSWAVE router version 2.4.11 - 'Hadron'
 Deployment complete, tailing log. Ctrl-C to quit.
...
```
After the service is deployed to Spawnpoint, log messages concerning the service
will appear on screen until the user presses `<CTRL>-C`.

#### Creating a Service Configuration
Like the Spawnpoint daemon, each service that runs on Spawnpoint can be
configured by writing a YAML file containing a sequence of key/value parameters.
The valid parameters currently are:

* `entity` (required): A file containing the entity that will be used as the
  service's identity in BOSSWAVE. This will be made available inside the
  service's Docker container as a file named `entity.key`. This file (or more
  precisely the path to this file) is also set as the value of the
  `BW2_DEFAULT_ENTITY` environment variable inside the container.

* `image` (optional): The complete name of a specific Docker image that is
  used to build the service's container. This image will then be pulled from
  the specified repository before service instantiation. Defaults to
  "jhkolb/spawnpoint:amd64".

* `aptRequires` (optional): A sequence of names of Ubuntu packages to install as
  part of the container build process.

* `source` (optional): A GitHub URI pointing to a repository to be checked out before
  starting the container. Must be HTTPS.

* `build` (optional): A sequence of commands to be run after checking out the
  necessary source code, e.g. for compilation.

* `run` (required): A sequence of commands to run the service, e.g. invoking a
  compiled binary.

* `memAlloc` (required): The amount of memory to reserve for this service. This
  should be expressed in MB, signified by a suffix of "M" or "m" (e.g. 512M) or
  in GB, signified by a suffix of "G" or "g" (e.g. "4G").

* `cpuShares` (required): The amount of CPU shares to reserve for this service.
  This should be 1024 shares per core, so requesting 2048 CPU shares is
  equivalent to requesting 2 cores.

* `autoRestart` (optional): A boolean specifying if this service's container
  should be automatically restarted upon termination. Defaults to `false`.

* `restartInt` (optional): If `autoRestart` is enabled, this specifies the
  amount of time wait after a service has terminated before attempting a
  restart. The normal Go duration syntax is used. For example, a value of "30s"
  signifies a 30-second delay, while a value of "5m" signifies a 5-minute delay.  

* `includedFiles` (optional): A list of paths to files that are to be included
  in the container. All files, regardless of their location on the machine from
  which `spawnctl` is invoked, will be placed in the current working directory
  of the code that runs inside the container, retaining their original names.

* `includedDirs` (optional): A list of paths to directories that are to be
  included in the container. Placement and naming is analogous to included
  files.

* `volumes` (optional): A list of names for volumes to be used by the container.
  If a volume does not exist on the host Spawnpoint, it will be created.
  Otherwise, the existing volume is attached to the container. All volumes are
  mounted in the container under the `srv` directory, so a volume named `foo`
  will be available to code running in the container as `/srv/foo`. This is
  intended as a means of preserving data between invocations of a service.

* `overlayNet` (optional): If the underlying Docker daemon supports it (see
  above), Docker containers running on Spawnpoint can be connected to an overlay
  network. All containers connected to a particular network can identify each
  other by name rather than IP address when using native means of communication.
  This is unnecessary for BOSSWAVE services.

For example, to run [demosvc](https://github.com/jkolb1/demosvc), the following
configuration could be used.

```yaml
entity: ~/bosswave/spawnpointTest.key
container: immesys/spawnpoint:amd64
build: [go get github.com/jkolb1/demosvc]
run: [demosvc, 100]
memAlloc: 512M
cpuShares: 1024
autoRestart: true
includedFiles: [params.yml]
volumes: [foo, bar]
```

#### Service Parameters
Parameters may no longer be included directly in service configuration files.
Instead, services may include arbitrary files to provide configuration or
bootstrapping information by using the `includedFiles` and `includedDirs`
fields. However, support for parsing a YAML file containing a sequence of
key/value attribute pairs is still supported by the `spawnable` library through
the `GetParams` function and its relatives.

### Restarting/Stopping a Service
To restart or stop a service, you must know the base URI of the Spawnpoint on
which it is running as well as its human readable name. To restart the service
`demosvc` on the Spawnpoint `scratch.ns/spawnpoint/alpha`:

```
$ ./spawnctl restart -u scratch.ns/spawnpoint/alpha -n demosvc
<snip> [Info] Connected to BOSSWAVE router version 2.4.7 - 'Hadron'
Monitoring log URI scratch.ns/spawnpoint/gamma/s.spawnpoint/demosvc/i.spawnable/signal/log. Ctrl-C to quit
[06/01 22:01:46] gamma::demosvc > attempting restart
...
```

To stop rather than restart `demosvc` on Spawnpoint alpha, simply replace
`restart` with `stop` above.

Note that, just as with a service deployment, log messages from the Spawnpoint
are emitted to the screen until the user presses `<CTRL>-C`.

When a Spawnpoint service terminates and does not have `autoRestart` enabled, it
enters a brief "zombie" period. During this time, the service will still appear
in `spawnctl` scan results, and you may restart the service like normal. Once
this period expires, the service is deleted, and you must do a manual deploy
operation to reinstantiate the service.

## Writing a Spawnpoint Service
The [demosvc](https://github.com/jkolb1/demosvc) provides a good example of a
simple Spawnpoint service. While Spawnpoint encourages the use of BOSSWAVE for
communication, it does not impose any other constraints on how a service
is implemented. However, service developers should bear in mind how the
containers for their services are set up:

* The service's BOSSWAVE entity is saved in the file `entity.key`, and is also
  accessible from the `BW2_DEFAULT_ENTITY` environment variable.

* Files specified in the `includedFiles` configuration parameter will be
  available in the working directory of code that runs in the container. All
  files retain their original names.

* Directories specified in the `includedDirs` configuration parameter, along
  with all child files and directories, will be available in the working
  directory of code that runs in the container. All directories retain their
  original names.

* Volumes specified in the `volumes` configuration parameter will be made
  available inside the container under `/srv`. Volumes persists beyond the
  termination of a container, but note that it is up to the user to keep track
  of which Spawnpoints contain the desired volumes.

Advice for writing drivers specifically can be found
[here](https://github.com/immesys/bw2/wiki/Drivers).

## Interacting Manually with a Spawnpoint
Each Spawnpoint publishes and subscribes to a particular set of BOSSWAVE URIs
based on its configured path. For example, a Spawnpoint with the path
`scratch.ns/spawnpoint/alpha` interacts with the following URIs.

**Subscribes To:**

* `scratch.ns/spawnpoint/alpha/s.spawnpoint/server/i.spawnpoint/slot/config`:
  accepts configurations for new services to deploy. Messages must
  contain a payload object of type 67.0.2.0, which is a YAML sequence of
  key/value configuration parameters for the new service.
* `scratch.ns/spawnpoint/alpha.s.spawnpoint/server/i.spawnpoint/slot/restart`:
  accepts commands to restart a currently executing service. Messages must
  contain a payload object of type 64.0.1.0 with a string containing the name of
  the service to restart.
* `scratch.ns/spawnpoint/alpha.s.spawnpoint/server/i.spawnpoint/slot/stop`:
  accepts commands to stop a currently executing service. Messages must
  contain a payload object of type 64.0.1.0 with a string containing the name of
  the service to stop.

**Publishes To:**

* `scratch.ns/spawnpoint/alpha/s.spawnpoint/server/i.spawnpoint/signal/heartbeat`:
  emits periodic heartbeat messages with information about the Spawnpoint: its
  alias, its total memory and CPU shares, and the amount of currently used
  memory and CPU shares. This information is stored in a PO of type 2.0.2.1,
  a MessagePack dictionary.
* `scratch.ns/spawnpoint/alpha/s.spawnpoint/server/i.spawnpoint/signal/heartbeat/<svc_name>`:
  emits periodic heartbeats about the running service with name `<svc_name>`.
  This information is stored in a PO of type 2.0.2.2, a MessagePack dictionary.
* `scratch.ns/spawnpoint/alpha/s.spawnpoint/server/i.spawnpoint/!meta/<key_name>`:
  emits periodic metdata values for the key `<key_name>`. This information is
  stored in a BOSSWAVE PO of type 2.0.3.1, a metadata tuple.
