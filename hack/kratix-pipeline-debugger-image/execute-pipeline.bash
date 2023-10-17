#!/usr/bin/env bash

set -euo pipefail

echo "Waiting for file /tmp/continue to exist before exiting"

until [ -f /tmp/continue ]
do
     echo -n "."
     sleep 3
done

echo "finished"
exit 0
