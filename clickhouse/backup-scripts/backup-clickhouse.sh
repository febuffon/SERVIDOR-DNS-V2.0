#!/usr/bin/env bash
# ============================================================
# backup-clickhouse.sh - Backup logico do schema + dados recentes do
# ClickHouse (dns_telemetry) via clickhouse-client, com rotacao.
#
# Nao faz backup completo do volume (grande demais / dados raw tem TTL
# de 30-90 dias por design). Aqui priorizamos:
#   1. Schema completo (DDL de tabelas e materialized views) - permite
#      recriar a estrutura do zero em caso de perda total.
#   2. Export das tabelas agregadas (baixo volume, alto valor: top_domains,
#      dns_queries_1min, etc) em formato Native comprimido.
#
# Nao versionado no Git - ver .gitignore.
# Agendar via cron do usuario, ex (diario as 03:30):
#   30 3 * * * /home/luiz-fernando/IA.CONFIG/clickhouse/backup-scripts/backup-clickhouse.sh >> /home/luiz-fernando/IA.CONFIG/clickhouse/backup-scripts/backup.log 2>&1
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/../.env"
BACKUP_DIR="${SCRIPT_DIR}/dumps"
RETENTION_DAYS=14
CONTAINER="clickhouse"
DB="dns_telemetry"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"

# Tabelas agregadas (baixo volume) que vale a pena exportar por completo.
# dns_queries (raw) fica de fora: alto volume e baixo valor de longo prazo.
AGG_TABLES=(dns_queries_1min top_domains top_clients nxdomain_tracker tld_distribution)

if [[ -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
fi
: "${CLICKHOUSE_PASSWORD:?CLICKHOUSE_PASSWORD nao definido (verifique clickhouse/.env)}"

if ! docker inspect -f '{{.State.Running}}' "$CONTAINER" >/dev/null 2>&1; then
    echo "[backup-clickhouse] erro: container $CONTAINER nao esta rodando" >&2
    exit 1
fi

OUT_DIR="${BACKUP_DIR}/${TIMESTAMP}"
mkdir -p "$OUT_DIR"

ch_client() {
    docker exec -i "$CONTAINER" clickhouse-client --user admin --password "${CLICKHOUSE_PASSWORD}" "$@"
}

echo "[backup-clickhouse] exportando schema (DDL) de ${DB}..."
{
    ch_client -q "SHOW CREATE DATABASE ${DB}" --format=TSVRaw
    for tbl in $(ch_client -q "SELECT name FROM system.tables WHERE database='${DB}'" --format=TSVRaw); do
        echo "-- ${tbl}"
        ch_client -q "SHOW CREATE TABLE ${DB}.${tbl}" --format=TSVRaw
        echo ";"
    done
} > "${OUT_DIR}/schema.sql"

for tbl in "${AGG_TABLES[@]}"; do
    OUT_FILE="${OUT_DIR}/${tbl}.native.zst"
    echo "[backup-clickhouse] exportando tabela ${tbl} -> ${OUT_FILE}"
    ch_client -q "SELECT * FROM ${DB}.${tbl} FORMAT Native" | zstd -q -o "$OUT_FILE"
done

tar -C "$BACKUP_DIR" -czf "${BACKUP_DIR}/clickhouse-${TIMESTAMP}.tar.gz" "${TIMESTAMP}"
rm -rf "$OUT_DIR"

SIZE=$(du -h "${BACKUP_DIR}/clickhouse-${TIMESTAMP}.tar.gz" | cut -f1)
echo "[backup-clickhouse] backup concluido | tamanho=${SIZE}"

echo "[backup-clickhouse] removendo backups com mais de ${RETENTION_DAYS} dias"
find "$BACKUP_DIR" -maxdepth 1 -name "clickhouse-*.tar.gz" -mtime "+${RETENTION_DAYS}" -print -delete

echo "[backup-clickhouse] finalizado"
