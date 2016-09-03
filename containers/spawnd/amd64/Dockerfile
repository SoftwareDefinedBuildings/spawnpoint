FROM ubuntu:xenial
MAINTAINER Jack Kolb <jkolb@berkeley.edu>

RUN apt-get update && apt-get install -y libssl-dev
ADD spawnd /usr/local/bin/spawnd
CMD /usr/local/bin/spawnd run -c /etc/spawnd/config.yml -m /etc/spawnd/metadata.yml
