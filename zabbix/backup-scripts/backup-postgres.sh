#!/usr/bin/env bash
# ============================================================
# backup-postgres.sh - Dump do PostgreSQL do Zabbix (hosts, triggers,
# dashboards, historico de eventos, configs) com rotacao.
#
# Nao versionado no Git (dados sensiveis / volumosos) - ver .gitignore.
# Agendar via cron do usuario, ex (diario as 03:00):
#   0 3 * * * /home/luiz-fernando/IA.CONFIG/zabbix/backup-scripts/backup-postgres.sh >> /home/luiz-fernando/IA.CONFIG/zabbix/backup-scripts/backup.log 2>&1
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/../.env"
BACKUP_DIR="${SCRIPT_DIR}/dumps"
RETENTION_DAYS=14
CONTAINER="zabbix-postgres"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"

if [[ -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
fi
: "${ZABBIX_DB_PASSWORD:?ZABBIX_DB_PASSWORD nao definido (verifique zabbix/.env)}"

mkdir -p "$BACKUP_DIR"

if ! docker inspect -f '{{.State.Running}}' "$CONTAINER" >/dev/null 2>&1; then
    echo "[backup-postgres] erro: container $CONTAINER nao esta rodando" >&2
    exit 1
fi

DUMP_FILE="${BACKUP_DIR}/zabbix-postgres-${TIMESTAMP}.sql.gz"

echo "[backup-postgres] iniciando dump -> ${DUMP_FILE}"
docker exec -e PGPASSWORD="${ZABBIX_DB_PASSWORD}" "$CONTAINER" \
    pg_dump -U zabbix -d zabbix --no-owner --no-privileges \
    | gzip -9 > "$DUMP_FILE"

SIZE=$(du -h "$DUMP_FILE" | cut -f1)
echo "[backup-postgres] dump concluido | tamanho=${SIZE}"

echo "[backup-postgres] removendo dumps com mais de ${RETENTION_DAYS} dias"
find "$BACKUP_DIR" -name "zabbix-postgres-*.sql.gz" -mtime "+${RETENTION_DAYS}" -print -delete

echo "[backup-postgres] finalizado"
