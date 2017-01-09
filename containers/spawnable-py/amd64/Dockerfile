FROM ubuntu:xenial
MAINTAINER Jack Kolb

RUN apt-get update && \
    apt-get dist-upgrade -y --no-install-recommends \
    git build-essential libssl-dev wget python python-pip
RUN pip --no-cache-dir install --upgrade pip
RUN pip --no-cache-dir install setuptools
RUN git clone https://github.com/SoftwareDefinedBuildings/bw2python && \
    pip --no-cache-dir install bw2python/
RUN apt-get clean && rm -rf /var/lib/apt/lists* /tmp/* /var/tmp/*
