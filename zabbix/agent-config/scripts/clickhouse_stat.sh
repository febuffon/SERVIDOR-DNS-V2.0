#!/bin/sh
# Extrai um valor especifico do endpoint Prometheus do ClickHouse.
# Uso: clickhouse_stat.sh <nome_da_metrica>
# Ex:  clickhouse_stat.sh ClickHouseMetrics_Query

METRIC="$1"
if [ -z "$METRIC" ]; then
    echo "0"
    exit 1
fi

VALUE=$(wget -qO- --timeout=5 http://127.0.0.1:9363/metrics 2>/dev/null \
    | grep "^${METRIC} " | awk '{print $2}')

if [ -z "$VALUE" ]; then
    echo "0"
else
    echo "$VALUE"
fi
