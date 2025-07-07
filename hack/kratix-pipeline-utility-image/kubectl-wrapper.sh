#!/bin/sh

if [ ! -f /tmp/kubectl-cli ]; then
	cp /bin/kubectl-cli /tmp/kubectl-cli
	chmod +x /tmp/kubectl-cli
fi
exec /tmp/kubectl-cli "$@"
