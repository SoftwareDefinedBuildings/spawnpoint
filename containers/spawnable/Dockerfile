FROM ubuntu:xenial
MAINTAINER Jack Kolb <jkolb@berkeley.edu>

RUN apt-get update && apt-get dist-upgrade -y
RUN apt-get install -y git build-essential wget curl libssl-dev
RUN wget -O /tmp/go.tar.gz https://dl.google.com/go/go1.10.linux-amd64.tar.gz && tar -xf /tmp/go.tar.gz -C /usr/local/ && rm /tmp/go.tar.gz && mkdir /srv/go
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/go/bin:/srv/target:/srv/go/bin GOPATH=/srv/go
RUN go get gopkg.in/immesys/bw2bind.v5
RUN go get github.com/immesys/bw2bind
