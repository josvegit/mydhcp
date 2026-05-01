#!/usr/bin/env bash
# Spawn and manage DHCP test clients as Alpine containers on dhcp-lab-net.
#
# Usage:
#   ./client.sh spawn [NAME]          start a client running udhcpc
#   ./client.sh kill  [NAME|all]      stop one or all client containers
#   ./client.sh list                  show running clients + their obtained IPs
#   ./client.sh logs  [NAME]          tail udhcpc output from a container

set -euo pipefail

NETWORK="dhcp-lab-net"
PREFIX="dhcp-client"
IMAGE="alpine:3.19"

cmd="${1:-help}"
arg="${2:-}"

case "$cmd" in
spawn)
    name="${arg:-$(printf '%04x' $RANDOM)}"
    cname="${PREFIX}-${name}"
    echo "starting client: $cname"
    docker run --rm -d \
        --name "$cname" \
        --privileged \
        --network "$NETWORK" \
        "$IMAGE" \
        sh -c 'iface=$(ip link | awk -F: "/^[0-9]+: (eth|ens|enp)/{print \$2; exit}" | tr -d " " | cut -d@ -f1); \
               echo "interface: $iface"; \
               udhcpc -i "$iface" -f -v 2>&1 | tee /tmp/dhcp.log; \
               echo "udhcpc exited, sleeping..."; \
               sleep 3600'
    echo "spawned: $cname"
    ;;

kill)
    if [[ "$arg" == "all" || -z "$arg" ]]; then
        containers=$(docker ps --filter "name=${PREFIX}" --format "{{.Names}}" 2>/dev/null || true)
        if [[ -z "$containers" ]]; then
            echo "no client containers running"
        else
            echo "$containers" | xargs docker stop
            echo "stopped all clients"
        fi
    else
        docker stop "${PREFIX}-${arg}" 2>/dev/null || docker stop "$arg"
    fi
    ;;

list)
    echo "running DHCP clients:"
    docker ps --filter "name=${PREFIX}" \
        --format "table {{.Names}}\t{{.Status}}\t{{.Networks}}"
    ;;

logs)
    if [[ -z "$arg" ]]; then
        # pick the first running client
        arg=$(docker ps --filter "name=${PREFIX}" --format "{{.Names}}" | head -1)
        if [[ -z "$arg" ]]; then
            echo "no clients running" >&2; exit 1
        fi
    else
        arg="${PREFIX}-${arg}"
    fi
    echo "tailing $arg ..."
    docker exec "$arg" tail -f /tmp/dhcp.log
    ;;

*)
    echo "usage: $0 spawn [NAME] | kill [NAME|all] | list | logs [NAME]"
    ;;
esac
