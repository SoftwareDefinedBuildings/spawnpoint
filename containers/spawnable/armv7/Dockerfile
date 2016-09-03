FROM armv7/armhf-ubuntu
MAINTAINER "Michael Andersen <michael@steelcode.com>"

RUN apt-get update && apt-get dist-upgrade -y
RUN apt-get install -y git build-essential vim wget curl libssl-dev
RUN apt-get install -y --no-install-recommends mercurial
RUN wget -O /tmp/go.tar.gz https://storage.googleapis.com/golang/go1.7.linux-armv6l.tar.gz && tar -xf /tmp/go.tar.gz -C /usr/local/ && rm /tmp/go.tar.gz && mkdir /srv/go
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/go/bin:/srv/target:/srv/go/bin GOPATH=/srv/go
RUN go get gopkg.in/immesys/bw2bind.v5
RUN go get github.com/immesys/bw2
