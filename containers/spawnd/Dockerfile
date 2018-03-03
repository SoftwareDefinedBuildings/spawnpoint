FROM ubuntu:xenial
MAINTAINER Jack Kolb <jkolb@berkeley.edu>

RUN apt-get update && apt-get install -y libssl-dev
ADD spawnd /usr/local/bin/spawnd
ENTRYPOINT ["/usr/local/bin/spawnd", "run", "-c", "/etc/spawnd/config.yml"]
