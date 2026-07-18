# DNS Recursivo de Alta Performance v2.0

Stack completa de DNS recursivo para ISPs com telemetria em tempo real, dashboards e interface SQL.

## Arquitetura

```
Clientes
    │
    ▼
Unbound DNS  (porta 53 / 5300)
    │ dnstap (socket Unix)
    ▼
dnstap-collector  (Go)
    │ INSERT em batch
    ▼
ClickHouse  (:8123 / :9000)
    │ Materialized Views automáticas
    ├── Grafana  (:3000)        — Dashboards em tempo real
    ├── ClickHouse UI  (:8080)  — Interface SQL interativa
    └── Prometheus  (:9363)     — Métricas para Zabbix
```

## Componentes

| Componente | Tecnologia | Porta |
|---|---|---|
| DNS Recursivo | Unbound 1.22 (Docker) | 53 / 5300 |
| Coletor dnstap | Go customizado (Docker) | — |
| Banco de dados | ClickHouse 24.6 (Docker) | 8123 / 9000 |
| Dashboards | Grafana 11.1 (Docker) | 3000 |
| Interface SQL | Nginx + HTML (Docker) | 8080 |
| Métricas | Prometheus endpoint | 9363 |

## Pré-requisitos

- Ubuntu 22.04 LTS
- Docker 24+
- Docker Compose v2
- 8 vCPU / 13GB RAM (mínimo recomendado)
- Disco NVMe

## Estrutura de arquivos

```
servidor-dns-v2.0/
├── clickhouse/
│   ├── docker-compose.yml       — Container ClickHouse
│   ├── config/
│   │   ├── config.xml           — Configuração principal (memória, merge, logs)
│   │   └── conf.d/
│   │       └── dns_tuning.xml   — Tuning DNS + endpoint Prometheus
│   └── initdb/
│       └── 01_schema.sql        — Schema completo (tabelas + Materialized Views)
├── dnstap-collector/
│   ├── main.go                  — Leitor do socket fstrm do Unbound
│   ├── inserter.go              — Batch insert no ClickHouse
│   ├── env.go                   — Helpers de variáveis de ambiente
│   ├── go.mod / go.sum          — Dependências Go
│   ├── Dockerfile               — Multi-stage build (scratch final)
│   └── docker-compose.yml       — Container do coletor
├── grafana/
│   ├── docker-compose.yml       — Container Grafana
│   ├── provisioning/
│   │   ├── datasources/
│   │   │   └── clickhouse.yml   — Datasource ClickHouse (auto-provisionado)
│   │   └── dashboards/
│   │       └── dns.yml          — Provider de dashboards
│   └── dashboards/
│       └── dns-overview.json    — Dashboard DNS Overview completo
├── clickhouse-ui/
│   ├── index.html               — Interface SQL com tema escuro
│   ├── nginx.conf               — Config do Nginx
│   └── docker-compose.yml       — Container Nginx
├── unbound/
│   ├── docker-compose.yml       — Container Unbound
│   └── conf/
│       └── unbound.conf         — Configuração Unbound + dnstap
├── .env.example                 — Modelo de variáveis de ambiente
├── .gitignore                   — Ignora .env e dados locais
└── README.md                    — Este arquivo
```

## Deploy passo a passo

### 1. Clone o repositório

```bash
git clone https://gitea.flexnet.in/luiz.fernando/servidor-dns-v2.0
cd servidor-dns-v2.0
```

### 2. Configure as senhas

```bash
cp .env.example .env
nano .env
```

Conteúdo do `.env`:
```env
CLICKHOUSE_PASSWORD=sua_senha_aqui
GRAFANA_ADMIN_PASSWORD=sua_senha_aqui
```

### 3. Tuning do host (como root)

```bash
sudo bash clickhouse/host-tuning.sh
```

### 4. Crie o diretório do socket dnstap

```bash
mkdir -p /opt/dnstap
```

### 5. Suba os serviços na ordem correta

```bash
# ClickHouse primeiro
cd clickhouse && docker compose up -d
# Aguarde ~60s ficar healthy
docker inspect --format='{{.State.Health.Status}}' clickhouse

# Coletor dnstap (cria o socket antes do Unbound)
cd ../dnstap-collector && docker compose up -d

# Unbound (conecta no socket ao iniciar)
cd ../unbound && docker compose up -d

# Grafana
cd ../grafana && docker compose up -d

# Interface SQL
cd ../clickhouse-ui && docker compose up -d
```

> **Importante:** o `dnstap-collector` deve subir **antes** do Unbound. O Unbound conecta no socket ao iniciar e não tenta reconectar sozinho.

### 6. Verifique o pipeline

```bash
# Testa DNS
dig @127.0.0.1 google.com A +short

# Verifica logs do coletor (deve mostrar "flush OK")
docker logs dnstap-collector --tail 10

# Verifica dados no ClickHouse
docker exec clickhouse clickhouse-client \
  --user admin --password SUA_SENHA \
  -q "SELECT count() FROM dns_telemetry.dns_queries"
```

## Acesso às interfaces

| Interface | URL | Login |
|---|---|---|
| **Grafana** | `http://SEU-IP:3000` | admin / sua_senha |
| **ClickHouse UI** | `http://SEU-IP:8080` | — |
| **ClickHouse Play** | `http://SEU-IP:8123/play` | admin / sua_senha |
| **Prometheus metrics** | `http://SEU-IP:9363/metrics` | — |

## Configuração do Unbound

Edite `unbound/conf/unbound.conf` e ajuste para sua rede:

```yaml
server:
    # Sua subnet de clientes
    access-control: 192.168.X.X/24 allow

# Zona interna (DNS split horizon)
forward-zone:
    name: "sua.zona.interna"
    forward-addr: SEU_DNS_INTERNO
```

## Tabelas ClickHouse

| Tabela | Descrição | Retenção |
|---|---|---|
| `dns_queries` | Todas as consultas (raw) | 30 dias |
| `dns_queries_1min` | Agregado por minuto | 90 dias |
| `top_domains` | Top domínios por hora | 30 dias |
| `top_clients` | Top clientes por hora | 30 dias |
| `nxdomain_tracker` | NXDOMAINs por cliente | 15 dias |
| `tld_distribution` | Distribuição por TLD | 30 dias |
| `v_stats_last_hour` | View resumo última hora | — |

## Dashboard Grafana — DNS Overview

| Painel | Descrição |
|---|---|
| Queries por Minuto | Volume total de consultas ao longo do tempo |
| NXDOMAIN por Minuto | Domínios inexistentes ao longo do tempo |
| Total Queries | Contador do período selecionado |
| NXDOMAIN | Total de domínios não encontrados |
| Latência Média | Tempo médio de resposta em µs |
| Clientes Únicos | IPs distintos que consultaram |
| SERVFAIL | Erros internos (deve ser zero) |
| Domínios Únicos | Domínios distintos consultados |
| Top 15 Domínios | Ranking com gauge visual |
| Top 15 Clientes | Ranking com % NXDOMAIN por IP |
| Distribuição RCODE | NOERROR / NXDOMAIN / SERVFAIL |
| Tipos de Query | A / AAAA / MX / TXT etc |
| Top TLDs | .com / .com.br / .net etc |
| NXDOMAINs por Cliente | Detecção de bots e malware |
| Latência por Minuto | Média e máximo ao longo do tempo |

## Queries úteis no ClickHouse

```sql
-- Últimas consultas em tempo real
SELECT ts, client_ip, qname, qtype, rcode, latency_us
FROM dns_telemetry.dns_queries
ORDER BY ts DESC LIMIT 20;

-- Top domínios da última hora
SELECT qname, count() AS total
FROM dns_telemetry.dns_queries
WHERE ts >= now() - INTERVAL 1 HOUR
GROUP BY qname ORDER BY total DESC LIMIT 20;

-- Clientes com mais NXDOMAIN (detecção de bots)
SELECT client_ip,
       countIf(rcode='NXDOMAIN') AS nxdomain,
       count() AS total
FROM dns_telemetry.dns_queries
WHERE ts >= now() - INTERVAL 1 HOUR
GROUP BY client_ip ORDER BY nxdomain DESC;

-- Resumo da última hora
SELECT * FROM dns_telemetry.v_stats_last_hour;
```

## Variáveis de ambiente

| Variável | Padrão | Descrição |
|---|---|---|
| `CLICKHOUSE_PASSWORD` | — | Senha do admin do ClickHouse |
| `GRAFANA_ADMIN_PASSWORD` | — | Senha do admin do Grafana |
| `SERVER_ID` | `dns-cgr01` | Identificador do servidor nos dashboards |
| `BATCH_SIZE` | `500` | Linhas por batch INSERT |
| `FLUSH_INTERVAL_MS` | `1000` | Intervalo máximo de flush (ms) |
| `DNSTAP_SOCKET` | `/var/dnstap/dnstap.sock` | Path do socket Unix |
