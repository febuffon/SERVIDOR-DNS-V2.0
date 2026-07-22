#!/usr/bin/env bash
# ============================================================
# host-tuning.sh - Tuning do host para a stack DNS + ClickHouse
# Servidor: Ubuntu 22.04 | 8 vCPU | 13GB RAM | NVMe
# Idempotente: pode ser executado quantas vezes for preciso.
# Uso: sudo bash clickhouse/host-tuning.sh
# ============================================================
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "Este script precisa rodar como root (sudo bash clickhouse/host-tuning.sh)" >&2
    exit 1
fi

echo "==> Aplicando tuning de sysctl para ClickHouse/Docker..."
SYSCTL_FILE="/etc/sysctl.d/99-clickhouse-tuning.conf"
cat > "$SYSCTL_FILE" <<'EOF'
# ClickHouse / stack DNS - tuning de host
# Gerado por clickhouse/host-tuning.sh - nao editar manualmente

# Reduz uso de swap sem desativa-lo (mantem headroom sob pressao de memoria)
vm.swappiness = 10

# Evita falhas de alocacao "silenciosas" em cargas de memoria intensa (ClickHouse/JVM-like)
vm.overcommit_memory = 1

# Aumenta backlog de conexoes de rede sob carga (DNS UDP + ClickHouse native)
net.core.somaxconn = 4096
net.core.netdev_max_backlog = 5000

# Buffers de rede maiores para batch inserts do coletor / queries do Grafana
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216

# Portas efemeras disponiveis para conexoes de saida (upstream DNS, ClickHouse)
net.ipv4.ip_local_port_range = 1024 65535

# TCP MTU probing (evita blackhole de PMTU no forward DoT/DoH futuro)
net.ipv4.tcp_mtu_probing = 1
EOF
sysctl --system > /dev/null
echo "    OK: $SYSCTL_FILE aplicado"

echo "==> Verificando Transparent Huge Pages (recomendado: madvise)..."
THP_PATH="/sys/kernel/mm/transparent_hugepage/enabled"
if [[ -f "$THP_PATH" ]]; then
    CURRENT_THP=$(grep -oP '(?<=\[)[a-z]+(?=\])' "$THP_PATH" || true)
    if [[ "$CURRENT_THP" != "madvise" && "$CURRENT_THP" != "never" ]]; then
        echo madvise > "$THP_PATH"
        echo "    THP ajustado para madvise"
    else
        echo "    OK: THP ja esta em '$CURRENT_THP'"
    fi
else
    echo "    aviso: $THP_PATH nao encontrado (kernel sem suporte a THP?)"
fi

echo "==> Ajustando limites de arquivos abertos (nofile) para containers..."
LIMITS_FILE="/etc/security/limits.d/99-clickhouse-tuning.conf"
cat > "$LIMITS_FILE" <<'EOF'
# ClickHouse / stack DNS - limites de processo
root soft nofile 262144
root hard nofile 262144
* soft nofile 262144
* hard nofile 262144
EOF
echo "    OK: $LIMITS_FILE aplicado (efetivo em novos logins/sessoes)"

echo "==> Verificando scheduler de disco (recomendado: none/mq-deadline para NVMe)..."
for dev in /sys/block/nvme*/queue/scheduler; do
    [[ -f "$dev" ]] || continue
    echo "    $dev -> $(cat "$dev")"
done

echo
echo "==> Tuning concluido. Resumo:"
sysctl vm.swappiness vm.overcommit_memory net.core.rmem_max net.core.wmem_max
echo "THP atual: $(cat "$THP_PATH" 2>/dev/null || echo 'n/a')"
