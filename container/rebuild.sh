#! /bin/bash

cp ../spawnd/spawnd .
docker build . --no-cache -t jhkolb/spawnd
docker push jhkolb/spawnd
