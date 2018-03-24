#! /usr/bin/env python
import boto3
import os
import sys

ROOT_BUCKET = "get.bw2.io"

release_version = os.getenv("SPAWNPOINT_VERSION")
if release_version is None:
    print "Error: Environment variable $SPAWNPOINT_VERSION is undefined"
    sys.exit(1)
release_category = release_version.split('.')[0] + ".x"

s3 = boto3.client('s3')
# Upload spawnd files
print "Pushing spawnd binary..."
s3.upload_file("spawnd", ROOT_BUCKET, "spawnd/{}/linux/amd64/{}/spawnd".\
        format(release_category, release_version))
print "Complete!"

print "Pushing spawnd.service..."
s3.upload_file("spawnd.service", ROOT_BUCKET, "spawnd/{}/linux/amd64/{}/spawnd.service".\
        format(release_category, release_version))
print "Complete!"

# Upload installer
print "Pushing installer.sh..."
s3.upload_file("installer.sh", ROOT_BUCKET, "spawnd/{}/linux/amd64/{}/installer.sh".\
        format(release_category, release_version))
s3.upload_file("installer.sh", ROOT_BUCKET, "spawnpoint")
print "Complete!"

# Upload spawnctl binaries
print "Pushing spawnctl binary..."
s3.upload_file("spawnctl", ROOT_BUCKET, "spawnctl/{}/linux/amd64/{}/spawnctl".\
        format(release_category, release_version))
print "Complete!"
