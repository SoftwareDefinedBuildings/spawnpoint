# Bosswave Watchdog

`bw2Mon` is a simple [watchdog](https://github.com/immesys/wd) implementation
for [Bosswave](https://github.com/immesys/bw2) messages. It monitors the
liveness of a Bosswave endpoint by subscribing to a URI and periodically kicking
a corresponding watchdog when a new message is received.

The interval at which watchdogs are kicked is configurable. Between kicks,
however, `bw2Mon` continues to receive Bosswave messages; it just ignores them.
Once a new message is received *and* a complete time interval has elapsed since
the previous kick, the watchdog is newly kicked. This prevents a lengthy
Bosswave message queue from building up while also decoupling the rate of
message publication from the rate of watchdog kicks.

## Command Line Parameters
  * `--entity`: The Bosswave entity to use for subscriptions. This defaults to
    the `BW2_DEFAULT_ENTITY` environment variable.
  * `--prefix`: The prefix for all watchdog names, e.g. `ucberkeley.sdb`.
  * `--interval`: The interval at which to kick watchdogs, specified as a
    standard Go duration string (e.g. `30s` for 30 seconds, `5m` for 5 minutes)
    Defaults to 2 minutes.
  * `--endpoint` (Variable Number): A Bosswave URI to subscribe to and the
    corresponding watchdog to kick, separated by a colon (`:`). For example:
    `scratch.ns/spawnpoint/alpha:spawnpoint.alpha`

## Spawnpoint Specifics
Spawnpoint heartbeat messages are published on:
`<base_uri>/s.spawnpoint/server/i.spawnpoint/signal/heartbeat`

Spawnpoint service heartbeats are published on:
`<base_uri>/s.spawnpoint/server/i.spawnpoint/signal/heartbeat/<svc_name>``

## Example
```
$ bw2Mon --entity myKey.ent --prefix ucberkeley.sdb --interval 3m \
  --endpoint "scratch.ns/thermostat/status:thermostat" \
  --endpoint "scratch.ns/lights/state:lights"
```

`bw2Mon` will subscribe to two URIs:
  1. `scratch.ns/thermostat/status`
  2. `scratch.ns/lights/state`

Every 3 minutes, a watchdog is kicked after a new message is received on a URI.
If the message is received on URI (1), `ucberkeley.sdb.thermostat` is kicked. If
a message is received on URI (2), `ucberkeley.sdb.lights` is kicked. Notice how
the `prefix` argument is prepended to each watchdog name specified in an
`endpoint` argument.
