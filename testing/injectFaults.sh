#! /bin/bash

if [ $# -lt 2 ]; then
    echo "Usage: injectFaults.sh <BW2 EC2-based DR IP> <SSH Key>"
    exit 1
fi

# Local agent failure
echo "Testing local agent failure..."
systemctl stop bw2
sleep 35s
systemctl start bw2
sleep 35s
echo "Complete"

# Remote router failure
echo "Testing remote designated router failure"
ssh -i $2 ubuntu@$1 << EOF
    sudo systemctl stop bw2
    sleep 35s
    sudo systemctl start bw2
EOF
sleep 35s
echo "Complete"

# Second remote router failure, induces local agent failure as well (Due to a bw2 bug)
# DR dies -> agent dies -> agent recovers -> DR recovers
echo "Testing second remote DR failure, induces local agent failure"
ssh -i $2 ubuntu@$1 << EOF
    sudo systemctl stop bw2
    sleep 1m
    sudo systemctl start bw2
EOF
sleep 35s
echo "Complete"

# Local agent failure followed by remote router failure
echo "Testing agent failure followed by DR failure"
systemctl stop bw2
sleep 20s
ssh -i $2 ubuntu@$1 << EOF
    sudo systemctl stop bw2
    sleep 35s
    sudo systemctl start bw2
EOF
sleep 30s
systemctl start bw2
sleep 35s
echo "Complete"
