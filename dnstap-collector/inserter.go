package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	dnstap "github.com/dnstap/golang-dnstap"
	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"
)

// Numero de tentativas de flush antes de descartar o batch, e o intervalo
// base entre elas (backoff exponencial simples: base, 2*base, 4*base...).
const (
	flushMaxRetries  = 3
	flushRetryBase   = 500 * time.Millisecond
	statsLogInterval = 60 * time.Second
)

// DNSRow representa uma linha a inserir em dns_telemetry.dns_queries
type DNSRow struct {
	Ts           time.Time
	ServerID     string
	MessageType  string
	ClientIP     string
	ClientPort   uint16
	Protocol     string
	Qname        string
	Qtype        string
	Qclass       string
	Rcode        string
	AnswerCount  uint8
	LatencyUs    uint32
	FromCache    uint8
	ResponseSize uint16
	DoFlag       uint8
	AdFlag       uint8
}

// pendingQuery guarda o instante da query aguardando a resposta correspondente
type pendingQuery struct {
	ts time.Time
}

// Inserter gerencia o batch insert no ClickHouse
type Inserter struct {
	conn          clickhouse.Conn
	serverID      string
	batchSize     int
	flushInterval time.Duration

	// Correlaciona CLIENT_QUERY -> CLIENT_RESPONSE para calcular latencia real,
	// já que o Unbound emite frames separados: query_time só vem no frame de
	// query e response_time só vem no frame de resposta.
	pending map[string]pendingQuery

	// Contadores de observabilidade (acessados por multiple goroutines: Run()
	// e o listener do socket em main.go) - expostos via log periodico e
	// disponiveis para health-check externo futuro (Zabbix/Prometheus).
	droppedFrames  atomic.Uint64 // frames descartados por canal cheio (main.go)
	failedRows     atomic.Uint64 // linhas perdidas apos esgotar retries de flush
	insertedRows   atomic.Uint64 // linhas inseridas com sucesso
}

func NewInserter(dsn, serverID string, batchSize int, flushInterval time.Duration) (*Inserter, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("DSN invalido: %w", err)
	}

	opts.DialTimeout = 10 * time.Second
	opts.ConnMaxLifetime = time.Hour
	opts.MaxOpenConns = 4
	opts.MaxIdleConns = 2
	opts.ConnOpenStrategy = clickhouse.ConnOpenInOrder

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("clickhouse.Open: %w", err)
	}

	// Testa conectividade com retry
	for i := 0; i < 10; i++ {
		if err := conn.Ping(context.Background()); err == nil {
			break
		}
		if i == 9 {
			return nil, fmt.Errorf("ClickHouse nao disponivel apos 10 tentativas")
		}
		log.Printf("[inserter] aguardando ClickHouse... tentativa %d/10", i+1)
		time.Sleep(3 * time.Second)
	}

	log.Printf("[inserter] conectado ao ClickHouse | batch=%d flush=%s", batchSize, flushInterval)

	return &Inserter{
		conn:          conn,
		serverID:      serverID,
		batchSize:     batchSize,
		flushInterval: flushInterval,
		pending:       make(map[string]pendingQuery),
	}, nil
}

func (ins *Inserter) Close() {
	ins.conn.Close()
}

// Run consome o canal de frames e faz flush em batch
func (ins *Inserter) Run(ctx context.Context, msgCh <-chan []byte) {
	batch := make([]DNSRow, 0, ins.batchSize)
	ticker := time.NewTicker(ins.flushInterval)
	defer ticker.Stop()

	statsTicker := time.NewTicker(statsLogInterval)
	defer statsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			if len(batch) > 0 {
				ins.flushWithRetry(batch)
			}
			return

		case frame := <-msgCh:
			row, corrKey, isQuery, err := parseFrame(frame, ins.serverID)
			if err != nil {
				continue
			}

			if isQuery {
				if corrKey != "" {
					ins.pending[corrKey] = pendingQuery{ts: row.Ts}
				}
			} else if corrKey != "" {
				if pq, ok := ins.pending[corrKey]; ok {
					if diff := row.Ts.Sub(pq.ts); diff > 0 {
						row.LatencyUs = uint32(diff.Microseconds())
					}
					delete(ins.pending, corrKey)
				}
				// Heuristica de cache hit: resposta recursiva (RA=1, AA=0)
				// resolvida muito rapido (<2ms) indica que veio do cache
				// local do Unbound, sem ida ao upstream.
				if row.Rcode != "-" && row.LatencyUs > 0 && row.LatencyUs < 2000 {
					row.FromCache = 1
				}
			}

			batch = append(batch, row)
			if len(batch) >= ins.batchSize {
				ins.flushWithRetry(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				ins.flushWithRetry(batch)
				batch = batch[:0]
			}
			ins.cleanupPending()

		case <-statsTicker.C:
			ins.logStats()
		}
	}
}

// logStats reporta contadores acumulados desde o inicio do processo -
// util para detectar perda de dados (canal cheio ou flush falhando) sem
// precisar grep-ar cada linha de log individual.
func (ins *Inserter) logStats() {
	dropped := ins.droppedFrames.Load()
	failed := ins.failedRows.Load()
	inserted := ins.insertedRows.Load()
	if dropped > 0 || failed > 0 {
		log.Printf("[inserter] stats | inserted=%d dropped_frames=%d failed_rows=%d",
			inserted, dropped, failed)
	} else {
		log.Printf("[inserter] stats | inserted=%d (sem perdas)", inserted)
	}
}

// cleanupPending remove entradas orfas (query sem resposta) mais antigas que 10s
func (ins *Inserter) cleanupPending() {
	cutoff := time.Now().Add(-10 * time.Second)
	for k, pq := range ins.pending {
		if pq.ts.Before(cutoff) {
			delete(ins.pending, k)
		}
	}
}

// flushWithRetry tenta o flush ate flushMaxRetries vezes com backoff
// exponencial antes de desistir do batch. Falhas transitorias de rede/
// ClickHouse (reinicio do container, timeout momentaneo) nao devem
// resultar em perda imediata de dados.
func (ins *Inserter) flushWithRetry(rows []DNSRow) {
	var err error
	for attempt := 0; attempt <= flushMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := flushRetryBase * time.Duration(1<<(attempt-1))
			log.Printf("[inserter] retry %d/%d em %s (batch=%d)", attempt, flushMaxRetries, backoff, len(rows))
			time.Sleep(backoff)
		}
		if err = ins.flush(rows); err == nil {
			ins.insertedRows.Add(uint64(len(rows)))
			return
		}
	}
	ins.failedRows.Add(uint64(len(rows)))
	log.Printf("[inserter] desistindo apos %d tentativas, %d linhas perdidas: %v", flushMaxRetries, len(rows), err)
}

// flush envia o batch para o ClickHouse. Retorna erro em caso de falha,
// sem logar diretamente (quem loga eh o chamador, que sabe o contexto de
// retry).
func (ins *Inserter) flush(rows []DNSRow) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	b, err := ins.conn.PrepareBatch(ctx,
		`INSERT INTO dns_telemetry.dns_queries
		 (ts, server_id, message_type, client_ip, client_port, protocol,
		  qname, qtype, qclass, rcode, answer_count, latency_us,
		  from_cache, response_size, do_flag, ad_flag)`)
	if err != nil {
		return fmt.Errorf("PrepareBatch: %w", err)
	}

	for _, r := range rows {
		if err := b.Append(
			r.Ts, r.ServerID, r.MessageType, r.ClientIP, r.ClientPort, r.Protocol,
			r.Qname, r.Qtype, r.Qclass, r.Rcode, r.AnswerCount, r.LatencyUs,
			r.FromCache, r.ResponseSize, r.DoFlag, r.AdFlag,
		); err != nil {
			log.Printf("[inserter] Append erro (linha ignorada): %v", err)
		}
	}

	if err := b.Send(); err != nil {
		return fmt.Errorf("Send (batch=%d): %w", len(rows), err)
	}

	log.Printf("[inserter] flush OK | %d rows", len(rows))
	return nil
}

// RecordDroppedFrame incrementa o contador de frames descartados por
// canal cheio (chamado a partir de main.go/handleConn).
func (ins *Inserter) RecordDroppedFrame() {
	ins.droppedFrames.Add(1)
}

// parseFrame converte um frame dnstap protobuf em DNSRow.
// Retorna também a chave de correlação query<->response (client_ip:port:dns_id)
// e se o frame é uma query (true) ou resposta (false).
func parseFrame(frame []byte, serverID string) (DNSRow, string, bool, error) {
	msg := &dnstap.Dnstap{}
	if err := proto.Unmarshal(frame, msg); err != nil {
		return DNSRow{}, "", false, fmt.Errorf("unmarshal: %w", err)
	}

	m := msg.GetMessage()
	if m == nil {
		return DNSRow{}, "", false, fmt.Errorf("mensagem dnstap vazia")
	}

	row := DNSRow{
		ServerID: serverID,
	}

	// Timestamp
	sec := m.GetQueryTimeSec()
	nsec := m.GetQueryTimeNsec()
	if sec == 0 {
		sec = m.GetResponseTimeSec()
		nsec = m.GetResponseTimeNsec()
	}
	row.Ts = time.Unix(int64(sec), int64(nsec)).UTC()
	if row.Ts.IsZero() {
		row.Ts = time.Now().UTC()
	}

	// Tipo de mensagem
	row.MessageType = m.GetType().String()

	// IP e porta do cliente
	qAddr := m.GetQueryAddress()
	rAddr := m.GetResponseAddress()
	if len(qAddr) > 0 {
		row.ClientIP = ipBytesToString(qAddr)
		row.ClientPort = uint16(m.GetQueryPort())
	} else if len(rAddr) > 0 {
		row.ClientIP = ipBytesToString(rAddr)
		row.ClientPort = uint16(m.GetResponsePort())
	}

	// Protocolo
	switch m.GetSocketProtocol() {
	case dnstap.SocketProtocol_UDP:
		row.Protocol = "UDP"
	case dnstap.SocketProtocol_TCP:
		row.Protocol = "TCP"
	case dnstap.SocketProtocol_DOT:
		row.Protocol = "DOT"
	case dnstap.SocketProtocol_DOH:
		row.Protocol = "DOH"
	default:
		row.Protocol = "UDP"
	}

	// Parse da mensagem DNS (query ou response)
	var wireMsg []byte
	var isResponse bool

	switch m.GetType() {
	case dnstap.Message_CLIENT_RESPONSE,
		dnstap.Message_RESOLVER_RESPONSE,
		dnstap.Message_FORWARDER_RESPONSE:
		wireMsg = m.GetResponseMessage()
		isResponse = true
	default:
		wireMsg = m.GetQueryMessage()
		isResponse = false
	}

	var dnsID uint16
	var haveDNSID bool

	if len(wireMsg) > 0 {
		dnsMsg := new(dns.Msg)
		if err := dnsMsg.Unpack(wireMsg); err == nil {
			dnsID = dnsMsg.Id
			haveDNSID = true

			// Extrai qname, qtype, qclass da secao Question
			if len(dnsMsg.Question) > 0 {
				q := dnsMsg.Question[0]
				row.Qname = q.Name
				row.Qtype = dns.TypeToString[q.Qtype]
				if row.Qtype == "" {
					row.Qtype = fmt.Sprintf("TYPE%d", q.Qtype)
				}
				row.Qclass = dns.ClassToString[q.Qclass]
				if row.Qclass == "" {
					row.Qclass = "IN"
				}
			}

			if isResponse {
				row.Rcode = dns.RcodeToString[dnsMsg.Rcode]
				if row.Rcode == "" {
					row.Rcode = fmt.Sprintf("RCODE%d", dnsMsg.Rcode)
				}
				row.AnswerCount = uint8(len(dnsMsg.Answer))
				row.ResponseSize = uint16(len(wireMsg))

				// Flags DNSSEC
				if dnsMsg.CheckingDisabled {
					row.DoFlag = 1
				}
				if dnsMsg.AuthenticatedData {
					row.AdFlag = 1
				}

				// FromCache e calculado em Run() apos a correlacao de latencia
				// (aqui ainda nao temos LatencyUs disponivel)
			} else {
				// Query: sem rcode ainda
				row.Rcode = "-"
			}
		}
	}

	// Defaults seguros
	if row.Qname == "" {
		row.Qname = "."
	}
	if row.Qtype == "" {
		row.Qtype = "A"
	}
	if row.Qclass == "" {
		row.Qclass = "IN"
	}
	if row.Rcode == "" {
		row.Rcode = "NOERROR"
	}

	// Normaliza qname: remove trailing dot para consistencia
	row.Qname = strings.TrimSuffix(row.Qname, ".")

	// Chave de correlacao query<->response: o Unbound emite frames separados
	// (CLIENT_QUERY so tem query_time, CLIENT_RESPONSE so tem response_time),
	// entao a latencia real e calculada no inserter usando client_ip:port:dns_id.
	var corrKey string
	if haveDNSID && row.ClientIP != "" {
		corrKey = fmt.Sprintf("%s:%d:%d", row.ClientIP, row.ClientPort, dnsID)
	}

	return row, corrKey, !isResponse, nil
}

// ipBytesToString converte bytes de IP (4 ou 16) para string
func ipBytesToString(b []byte) string {
	switch len(b) {
	case 4:
		return net.IP(b).String()
	case 16:
		// IPv4-mapped IPv6: retorna IPv4
		if ip := net.IP(b).To4(); ip != nil {
			return ip.String()
		}
		return net.IP(b).String()
	default:
		if len(b) == 4 {
			return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
		}
		return fmt.Sprintf("unknown(%d bytes)", len(b))
	}
}

// uint16FromBytes converte big-endian bytes para uint16
func uint16FromBytes(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}
