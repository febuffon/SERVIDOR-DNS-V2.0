#!/usr/bin/env bash
# ============================================================
# start-stack.sh - Sobe a stack completa (DNS + monitoramento) na
# ordem correta. Usado pelo systemd (dns-stack.service) no boot, mas
# pode ser rodado manualmente a qualquer momento (idempotente: "docker
# compose up -d" nao recria containers ja rodando e saudaveis).
#
# Ordem critica:
#   1. ClickHouse       - precisa estar healthy antes do coletor conectar
#   2. dnstap-collector - precisa criar o socket ANTES do Unbound subir
#   3. Unbound           - conecta no socket dnstap ao iniciar (nao retenta)
#   4. Grafana / ClickHouse UI - sem dependencia critica de ordem
#   5. Zabbix            - monitoramento, sobe por ultimo
# ============================================================
set -euo pipefail

BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_PREFIX="[start-stack]"

log() {
    echo "${LOG_PREFIX} $(date '+%Y-%m-%d %H:%M:%S') - $*"
}

wait_healthy() {
    local container="$1"
    local timeout="${2:-90}"
    local elapsed=0
    log "aguardando '${container}' ficar healthy (timeout ${timeout}s)..."
    while [[ $elapsed -lt $timeout ]]; do
        status=$(docker inspect --format='{{.State.Health.Status}}' "$container" 2>/dev/null || echo "missing")
        if [[ "$status" == "healthy" ]]; then
            log "'${container}' healthy"
            return 0
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    log "aviso: '${container}' nao ficou healthy em ${timeout}s (status=${status}), seguindo mesmo assim"
    return 0
}

up() {
    local dir="$1"
    log "subindo stack em ${dir}..."
    (cd "${BASE_DIR}/${dir}" && docker compose up -d)
}

# 1. ClickHouse primeiro
up clickhouse
wait_healthy clickhouse 120

# 2. Coletor dnstap (cria o socket antes do Unbound)
up dnstap-collector
wait_healthy dnstap-collector 30

# 3. Unbound (conecta no socket dnstap ao iniciar)
up unbound
wait_healthy unbound 60

# 4. Interfaces de visualizacao
up grafana
up clickhouse-ui

# 5. Monitoramento por ultimo
up zabbix

log "stack completa iniciada"
