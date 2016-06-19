# Spawnpoint

Spawnpoint is a tool to deploy, run, and monitor services that communicate over
[BOSSWAVE](https://github.com/immesys/bw2). A "Spawnpoint" is an individual
machine that supports service execution by providing storage, a BOSSWAVE router
for communication, and a pool of memory and compute resources. Individual
services run in dedicated Docker containers that are instantiated and managed by
a Spawnpoint.

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
  the Spawnpoint/BOSSWAVE ecosystem. These utilities help with parsing
  Spawnpoint parameters (more on this below), manipulating metadata, and
  interacting with BOSSWAVE.

## Running Your Own Spawnpoint
To allow BOSSWAVE services to run on your own machine, you will need to:

1. Configure a local Spawnpoint
2. Run a `spawnd` process on your machine

### Configuring a Spawnpoint
To configure a Spawnpoint daemon, create a simple YAML file. This file will
contain a collection of key/value pairs that serve as parameters for Spawnpoint.
Currently, the valid parameters are:

* `entity` (required): A file containing the entity that will serve as the Spawnpoint's
  identity to BOSSWAVE. It is used by the Spawnpoint daemon to publish and
  subscribe to BOSSWAVE URIs.

* `alias` (required): A short and human readable name for the Spawnpoint.

* `path` (required): The Spawnpoint's base URI. It should uniquely identify the Spawnpoint
  and is used to form the URIs for the Spawnpoint's control and logging.

* `localRouter` (optional): An address and port for the Spawnpoint's designated BOSSWAVE
  router. Defaults to `localhost:28589`.

* `memAlloc` (required): The total amount of memory (in MB) to be used by
  spawned services.

* `cpuShares` (required): The total number of CPU shares to be used by spawned
  services. This is conventionally 1024 shares per core.

A typical Spawnpoint configuration file might look as follows. Note that the
`localRouter` parameter is omitted, and thus it takes on the default value.
```yaml
entity: ~/bosswave/spawnpointTest.key
path: scratch.ns/spawnpoint/alpha
alias: alpha
memAlloc: 4G
cpuShares: 4096
```

### Running the Spawnpoint Daemon
Use `go build` to compile the Spawnpoint daemon binary from its Go source code.
You can then run `./spawnd run -c spawnConfig.yml` to run a Spawnpoint according
to the configuration given in the file `spawnConfig.yml`. If no `-c` flag is
used, Spawnpoint will by default look for a file named `config.yml` in the
current directory.

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
$ ./spawnctl scan -u scratch.ns/spawnpoint
<snip> [Info] Connected to BOSSWAVE router version 2.2.2 - 'Fermion'
Discovered 2 SpawnPoints:
[beta] seen 24 May 16 23:39 PDT (189h30m36.428869615s) ago at <snip>/spawnpoint/beta
    Available Memory: 16384 MB, Available Cpu Shares: 8192
[alpha] seen 01 Jun 16 12:19 PDT (8h50m34.559646724s) ago at <snip>/spawnpoint/alpha
    Available Memory: 4096 MB, Available Cpu Shares: 4096
```

If your scan only matches one Spawnpoint, information about the services running
on that Spawnpoint will be automatically printed out. For example:
```
$ ./spawnctl scan -u scratch.ns/spawnpoint/alpha
<snip> [Info] Connected to BOSSWAVE router version 2.2.2 - 'Fermion'
Discovered 1 SpawnPoint:
[alpha] seen 01 Jun 16 12:19 PDT (8h55m12.820928399s) ago at <snip>/spawnpoint/alpha
    Available Memory: 4096 MB, Available Cpu Shares: 4096
    [demosvc] seen 01 Jun 16 12:17 PDT (8h56m53.051012245s) ago
        Memory: 512 MB, Cpu Shares: 1024
```

### Deploying a Service
#### Creating a Service Configuration
Like the Spawnpoint daemon, each service that runs on Spawnpoint can be
configured by writing a YAML file containing sequence of key/value parameters.
The valid parameters currently are:

* `entity` (required): A file containing the entity that will be used as the
  service's identity in BOSSWAVE. This will be made available inside the
  service's Docker container as a file named `entity.key`. This file is also
  defined as the value of the `BW2_DEFAULT_ENTITY` container environment
  variable.

* `container` (optional): The name of a specific Docker image that is used to
  build the service's container. Defaults to "immesys/spawnpoint:amd64".

* `aptRequires` (optional): A sequence of names of Ubuntu packages to install as
  part of the container build process.

* `source` (optional): A GitHub URI pointing to a repository to be checked out before
  starting the container. Must be HTTPS.

* `build` (required): A sequence of commands to be run after checking out the
  necessary source code, e.g. for compilation.

* `run` (required): A sequence of commands to run the service, e.g. invoking a
  compiled binary.

* `memAlloc` (required): The amount of memory, in MB, to reserve for this
  service.

* `cpuShares` (required): The amount of CPU shares to reserve for this service.

* `autoRestart` (optional): A boolean specifying if this service's container
  should be automatically restarted upon failure. Defaults to `false`.

* `params` (optional): Miscellaneous parameters for use by the service. These
  will be made available inside the Docker container in the `params.yml` file.

One or more related service configurations are then combined together into a
single YAML file, containing a sequence of key/value pairs. Each key is an alias
for an existing Spawnpoint, while each value is a configuration for a service to
be run on that Spawnpoint.

For example, to run [demosvc](https://github.com/jkolb1/demosvc) on a Spawnpoint
with alias `alpha`, the following configuration could be used. As demonstrated
below, basic Go-style templating can be used in configuration YAML files.

```yaml
{{$base := "jkolb/demo"}}
alpha:
  demosvc:
    entity: ~/bosswave/spawnpointTest.key
    container: immesys/spawnpoint:amd64
    build: [go get -u github.com/jkolb1/demosvc]
    run: [demosvc, 100]
    params:
      msg: "Hello, World"
      to: {{$base}}/out
    memAlloc: 512M
    cpuShares: 1024
    autoRestart: true
```
#### Installing the Service at a Spawnpoint
Use the `deploy` command to install and run services according to a particular
configuration at one or more spawnpoints. For example, if we want to use the
above configuration file, named `config.yml`, for our deployment:

```
$ spawnctl deploy -u scratch.ns/spawnpoint -c example.yml
<snip> [Info] Connected to BOSSWAVE router version 2.2.2 - 'Fermion'
 !! FINISHED DEPLOYMENT, TAILING LOGS. CTRL-C TO QUIT !!
...
```

After configurations have been deployed to the relevant Spawnpoint instances,
log messages from those Spawnpoints concerning these services will appear
on the screen until the user presses `<CTRL>-C`.

#### Defining Parameters in a Separate File
You may also define the parameters for a Spawnpoint service in an external file
rather than directly in the service's configuration file. This facilitates the
deployment of many services with the same configuration but different
parameters.

First, you will need to create a YAML file with the parameters for each service
you wish to deploy. In the case of the `demosvc` example, that file would be as
follows:
```yaml
{{$base := "jkolb/demo"}}
demosvc:
  msg: "Hello, World"
  to: {{$base}}/out
```
Note that you can use the same templating tools as with configuration files, as
demonstrated by the `base` variable above.

Next, you will need to pass an extra flag, `-p` or `--params`, to the `deploy`
command for `spawnctl`. Modifying the `deploy` command would give us:
```
$ spawnctl deploy -u scratch.ns/spawnpoint -c example.yml -p params.yml
```

### Restarting/Stopping a Service
To restart or stop a service, you must know the base URI of the Spawnpoint on
which it is running as well as its human readable name. To restart the service
`demosvc` on the Spawnpoint `scratch.ns/spawnpoint/alpha`:

```
$ ./spawnctl restart -u scratch.ns/spawnpoint/alpha -n demosvc
<snip> [Info] Connected to BOSSWAVE router version 2.2.2 - 'Fermion'
Monitoring log URI scratch.ns/spawnpoint/gamma/s.spawnpoint/demosvc/i.spawnable/signal/log. Ctrl-C to quit
[06/01 22:01:46] gamma::demosvc > attempting restart
...
```

To stop rather than restart `demosvc` on Spawnpoint alpha, simply replace
`restart` with `stop` above.

Note that, just like with service deployment, log messages from the Spawnpoint
are emitted to the screen until the user presses `<CTRL>-C`.

## Writing a Spawnpoint Service
The [demosvc](https://github.com/jkolb1/demosvc) provides a good example of a
simple Spawnpoint service. Aside from the fact that they should use BOSSWAVE for
communication, Spawnpoint does not impose any other constraints on how a service
is implemented. However, service developers should bear in mind how the
containers for their services are set up:

* The service's BOSSWAVE entity is saved in the file `entity.key`, and is also
  accessible from the `BW2_DEFAULT_ENTITY` environment variable.

* Parameters specified in the service's deployment configuration are contained
  in the file `params.yml`, which is located in the same directory as the
  service's source and/or binary files.

Advice for writing drivers specifically can be found
[here](https://github.com/immesys/bw2/wiki/Drivers).

## Interacting With a Spawnpoint Manually
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
  contain a payload object of type 64.0.0.0 with text containing the name of the
  service to restart.
* `scratch.ns/spawnpoint/alpha.s.spawnpoint/server/i.spawnpoint/slot/stop`:
  accepts commands to stop a currently executing service. Messages must
  contain a payload object of type 64.0.0.0 with text containing the name of the
  service to stop.

**Publishes To:**

* `scratch.ns/spawnpoint/alpha/s.spawnpoint/server/i.spawnpoint/signal/heartbeat`:
  emits periodic heartbeat messages with information about the Spawnpoint: its
  alias, its total memory and CPU shares, and the amount of currently used
  memory and CPU shares. This information is stored in a PO of type 2.0.2.1,
  which is a MessagePack dictionary.
