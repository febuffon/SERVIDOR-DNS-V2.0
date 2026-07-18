-- ============================================================
-- Schema DNS Telemetry - ClickHouse 24.6
-- Compativel com dnstap (Unbound) -> coletor -> ClickHouse
-- ============================================================

CREATE DATABASE IF NOT EXISTS dns_telemetry;

-- ============================================================
-- TABELA PRINCIPAL: dns_queries
-- ============================================================
CREATE TABLE IF NOT EXISTS dns_telemetry.dns_queries
(
    ts              DateTime64(3, 'America/Sao_Paulo') CODEC(Delta, LZ4),
    server_id       LowCardinality(String),
    message_type    LowCardinality(String),
    client_ip       String CODEC(ZSTD(1)),
    client_port     UInt16,
    protocol        LowCardinality(String),
    qname           String CODEC(ZSTD(3)),
    qtype           LowCardinality(String),
    qclass          LowCardinality(String),
    rcode           LowCardinality(String),
    answer_count    UInt8,
    latency_us      UInt32,
    from_cache      UInt8,
    response_size   UInt16,
    do_flag         UInt8,
    ad_flag         UInt8,
    date            Date MATERIALIZED toDate(ts)
)
ENGINE = MergeTree()
PARTITION BY toYYYYMMDD(ts)
ORDER BY (server_id, toStartOfMinute(ts), client_ip, qname)
TTL date + INTERVAL 30 DAY DELETE
SETTINGS
    index_granularity = 8192,
    min_bytes_for_wide_part = 10485760,
    merge_with_ttl_timeout = 3600;


-- ============================================================
-- TABELA AGREGADA: dns_queries_1min
-- ============================================================
CREATE TABLE IF NOT EXISTS dns_telemetry.dns_queries_1min
(
    bucket          DateTime('America/Sao_Paulo'),
    server_id       LowCardinality(String),
    qtype           LowCardinality(String),
    rcode           LowCardinality(String),
    protocol        LowCardinality(String),
    total_queries   UInt64,
    cache_hits      UInt64,
    nxdomain_count  UInt64,
    servfail_count  UInt64,
    sum_latency_us  UInt64,
    max_latency_us  UInt32,
    date            Date MATERIALIZED toDate(bucket)
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(bucket)
ORDER BY (server_id, bucket, qtype, rcode, protocol)
TTL date + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS dns_telemetry.mv_dns_queries_1min
TO dns_telemetry.dns_queries_1min
AS
SELECT
    toStartOfMinute(ts)              AS bucket,
    server_id,
    qtype,
    rcode,
    protocol,
    count()                          AS total_queries,
    countIf(from_cache = 1)          AS cache_hits,
    countIf(rcode = 'NXDOMAIN')      AS nxdomain_count,
    countIf(rcode = 'SERVFAIL')      AS servfail_count,
    sum(latency_us)                  AS sum_latency_us,
    max(latency_us)                  AS max_latency_us
FROM dns_telemetry.dns_queries
GROUP BY
    server_id,
    toStartOfMinute(ts),
    qtype,
    rcode,
    protocol;


-- ============================================================
-- TABELA: top_domains
-- ============================================================
CREATE TABLE IF NOT EXISTS dns_telemetry.top_domains
(
    bucket          DateTime('America/Sao_Paulo'),
    server_id       LowCardinality(String),
    qname           String CODEC(ZSTD(3)),
    qtype           LowCardinality(String),
    rcode           LowCardinality(String),
    total_queries   UInt64,
    date            Date MATERIALIZED toDate(bucket)
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(bucket)
ORDER BY (server_id, bucket, qname, qtype, rcode)
TTL date + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS dns_telemetry.mv_top_domains
TO dns_telemetry.top_domains
AS
SELECT
    toStartOfHour(ts)   AS bucket,
    server_id,
    qname,
    qtype,
    rcode,
    count()             AS total_queries
FROM dns_telemetry.dns_queries
GROUP BY server_id, toStartOfHour(ts), qname, qtype, rcode;


-- ============================================================
-- TABELA: top_clients
-- ============================================================
CREATE TABLE IF NOT EXISTS dns_telemetry.top_clients
(
    bucket          DateTime('America/Sao_Paulo'),
    server_id       LowCardinality(String),
    client_ip       String CODEC(ZSTD(1)),
    total_queries   UInt64,
    nxdomain_count  UInt64,
    date            Date MATERIALIZED toDate(bucket)
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(bucket)
ORDER BY (server_id, bucket, client_ip)
TTL date + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS dns_telemetry.mv_top_clients
TO dns_telemetry.top_clients
AS
SELECT
    toStartOfHour(ts)               AS bucket,
    server_id,
    client_ip,
    count()                         AS total_queries,
    countIf(rcode = 'NXDOMAIN')     AS nxdomain_count
FROM dns_telemetry.dns_queries
GROUP BY server_id, toStartOfHour(ts), client_ip;


-- ============================================================
-- TABELA: nxdomain_tracker
-- ============================================================
CREATE TABLE IF NOT EXISTS dns_telemetry.nxdomain_tracker
(
    bucket          DateTime('America/Sao_Paulo'),
    server_id       LowCardinality(String),
    client_ip       String CODEC(ZSTD(1)),
    qname           String CODEC(ZSTD(3)),
    cnt             UInt64,
    date            Date MATERIALIZED toDate(bucket)
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(bucket)
ORDER BY (server_id, bucket, client_ip, qname)
TTL date + INTERVAL 15 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS dns_telemetry.mv_nxdomain_tracker
TO dns_telemetry.nxdomain_tracker
AS
SELECT
    toStartOfHour(ts)   AS bucket,
    server_id,
    client_ip,
    qname,
    count()             AS cnt
FROM dns_telemetry.dns_queries
WHERE rcode = 'NXDOMAIN'
GROUP BY server_id, toStartOfHour(ts), client_ip, qname;


-- ============================================================
-- TABELA: tld_distribution
-- ============================================================
CREATE TABLE IF NOT EXISTS dns_telemetry.tld_distribution
(
    bucket          DateTime('America/Sao_Paulo'),
    server_id       LowCardinality(String),
    tld             LowCardinality(String),
    total_queries   UInt64,
    date            Date MATERIALIZED toDate(bucket)
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(bucket)
ORDER BY (server_id, bucket, tld)
TTL date + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS dns_telemetry.mv_tld_distribution
TO dns_telemetry.tld_distribution
AS
SELECT
    toStartOfHour(ts)                                   AS bucket,
    server_id,
    lower(arrayElement(splitByChar('.', qname), -1))    AS tld,
    count()                                             AS total_queries
FROM dns_telemetry.dns_queries
WHERE length(qname) > 0
GROUP BY server_id, toStartOfHour(ts), tld;


-- ============================================================
-- VIEW: v_stats_last_hour
-- ============================================================
CREATE VIEW IF NOT EXISTS dns_telemetry.v_stats_last_hour
AS
SELECT
    server_id,
    count()                                          AS total_queries,
    countIf(from_cache = 1)                          AS cache_hits,
    round(countIf(from_cache=1)/count()*100, 2)      AS cache_hit_rate_pct,
    countIf(rcode = 'NXDOMAIN')                      AS nxdomain,
    countIf(rcode = 'SERVFAIL')                      AS servfail,
    round(avg(latency_us), 0)                        AS avg_latency_us,
    max(latency_us)                                  AS max_latency_us,
    uniq(client_ip)                                  AS unique_clients,
    uniq(qname)                                      AS unique_domains
FROM dns_telemetry.dns_queries
WHERE ts >= now() - INTERVAL 1 HOUR
GROUP BY server_id
ORDER BY server_id;
