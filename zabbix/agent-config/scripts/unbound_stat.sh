#!/bin/sh
# Extrai um valor especifico das estatisticas do Unbound via unbound-control.
# Uso: unbound_stat.sh <chave>
# Ex:  unbound_stat.sh total.num.cachehits

KEY="$1"
if [ -z "$KEY" ]; then
    echo "0"
    exit 1
fi

VALUE=$(unbound-control -c /etc/unbound/unbound.conf stats_noreset 2>/dev/null | grep "^${KEY}=" | cut -d= -f2)

if [ -z "$VALUE" ]; then
    echo "0"
else
    echo "$VALUE"
fi
