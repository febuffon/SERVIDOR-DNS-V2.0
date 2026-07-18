# DNS Recursivo de Alta Performance v2.0

Stack completa de DNS recursivo para ISPs com telemetria em tempo real.

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
    │ Materialized Views
    ├── Grafana  (:3000)   — Dashboards
    └── ClickHouse UI  (:8080)   — SQL interativo
```

## Componentes

| Componente | Tecnologia | Porta |
|---|---|---|
| DNS Recursivo | Unbound 1.22 | 53 / 5300 |
| Coletor dnstap | Go (custom) | — |
| Banco de dados | ClickHouse 24.6 | 8123 / 9000 |
| Dashboards | Grafana 11.1 | 3000 |
| Interface SQL | Nginx + HTML | 8080 |
| Métricas | Prometheus | 9363 |

## Pré-requisitos

- Ubuntu 22.04 LTS
- Docker 24+
- Docker Compose v2

## Deploy rápido

```bash
# 1. Clone o repositório
git clone https://github.com/febuffon/IA.CONFIG
cd IA.CONFIG/servidor-dns-v2.0

# 2. Configure as senhas
cp .env.example .env
nano .env

# 3. Aplique o tuning do host (como root)
sudo bash clickhouse/host-tuning.sh

# 4. Crie o diretório do socket dnstap
mkdir -p /opt/dnstap

# 5. Suba o ClickHouse primeiro
cd clickhouse && docker compose up -d
# Aguarde ~60s ficar healthy

# 6. Suba o coletor dnstap
cd ../dnstap-collector && docker compose up -d

# 7. Suba o Unbound (APÓS o coletor criar o socket)
cd ../unbound && docker compose up -d

# 8. Suba o Grafana
cd ../grafana && docker compose up -d

# 9. Suba a interface ClickHouse
cd ../clickhouse-ui && docker compose up -d
```

## Acesso

| Interface | URL | Login |
|---|---|---|
| Grafana | http://SEU-IP:3000 | admin / sua_senha |
| ClickHouse UI | http://SEU-IP:8080 | — |
| ClickHouse API | http://SEU-IP:8123/play | admin / sua_senha |

## Configuração do Unbound

Edite `unbound/conf/unbound.conf` e ajuste:

```yaml
# Sua rede de clientes
access-control: 192.168.X.X/24 allow

# DNS interno (zona privada)
forward-zone:
    name: "sua.zona.interna"
    forward-addr: SEU_DNS_INTERNO
```

## Ordem de inicialização

**Importante:** o coletor dnstap deve subir **antes** do Unbound para criar o socket Unix. O Unbound conecta no socket ao iniciar.

```
ClickHouse → dnstap-collector → Unbound → Grafana
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

## Dashboard Grafana — DNS Overview

- Queries por minuto / NXDOMAIN por minuto
- Cards: Total, NXDOMAIN, Latência, Clientes únicos, SERVFAIL, Domínios únicos
- Top 15 Domínios e Top 15 Clientes (com gauge visual)
- Distribuição por RCODE, Tipo de Query e TLD
- Tabela de NXDOMAINs por cliente (detecção de bots)
- Latência média e máxima por minuto

## Estrutura de arquivos

```
servidor-dns-v2.0/
├── clickhouse/
│   ├── docker-compose.yml
│   ├── config/
│   │   ├── config.xml
│   │   └── conf.d/dns_tuning.xml
│   └── initdb/
│       └── 01_schema.sql
├── dnstap-collector/
│   ├── main.go
│   ├── inserter.go
│   ├── env.go
│   ├── go.mod
│   ├── Dockerfile
│   └── docker-compose.yml
├── grafana/
│   ├── docker-compose.yml
│   ├── provisioning/
│   │   ├── datasources/clickhouse.yml
│   │   └── dashboards/dns.yml
│   └── dashboards/
│       └── dns-overview.json
├── clickhouse-ui/
│   ├── index.html
│   ├── nginx.conf
│   └── docker-compose.yml
├── unbound/
│   ├── docker-compose.yml
│   └── conf/
│       └── unbound.conf
├── .env.example
├── .gitignore
└── README.md
```
