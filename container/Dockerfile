FROM ubuntu:xenial
MAINTAINER Jack Kolb <jkolb@berkeley.edu>

# Install packages
RUN apt-get update && apt-get dist-upgrade -y && \
    apt-get install -y git build-essential wget curl libssl-dev

# Set up golang
RUN wget -O /tmp/go.tar.gz \
    https://storage.googleapis.com/golang/go1.6.2.linux-amd64.tar.gz && \
    tar -xf /tmp/go.tar.gz -C /usr/local/ && \
    rm /tmp/go.tar.gz && mkdir /srv/go
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/go/bin:/srv/target:/srv/go/bin GOPATH=/srv/go

RUN go get github.com/immesys/spawnpoint/spawnd && rm -rf $GOPATH/src $GOPATH/pkg

CMD spawnd run -c /etc/spawnd/config.yml
