#! /usr/bin/env python
import re
import sys

if len(sys.argv) < 3:
    print "Usage: {} <service config file> <output file>"
    sys.exit(1)
daemon_config_file = sys.argv[1]
output_file = sys.argv[2]

deprecated_fields = ["restartInt", "overlayNet"]

with open(daemon_config_file) as f:
    with open(output_file, 'w') as g:
        for line in f:
            line = line.strip()

            # Remove deprecated fields
            if any([line.startswith(x) for x in deprecated_fields]):
                continue

            # Rename 'entity' to 'bw2Entity'
            if line.startswith("entity"):
                line = line.replace("entity", "bw2Entity", 1)

            # Rename 'memAlloc' to 'memory' and convert to MiB
            elif line.startswith("memAlloc"):
                match = re.match(r'memAlloc\s*:\s*(\d+)([MmGg])', line)
                if match is None:
                    raise ValueError("Invalid memory allocation: {}".format(line))
                memQuantity = int(match.group(1))
                memUnits = match.group(2)
                if memUnits == "G" or memUnits == "g":
                    memQuantity *= 1024
                line = "memory: {}".format(memQuantity)

            # Rename image 'jhkolb/spawnpoint:amd64' to 'jhkolb/spawnable:amd64'
            elif line.startswith("image"):
                line = line.replace("jhkolb/spawnpoint:amd64", "jhkolb/spawnable:amd64")

            # Rename 'includedDirs' to 'includedDirectories'
            elif line.startswith("includedDirs"):
                line = line.replace("includedDirs", "includedDirectories", 1)

            g.write(line + "\n")
