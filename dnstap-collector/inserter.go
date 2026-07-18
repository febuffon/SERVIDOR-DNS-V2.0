package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	dnstap "github.com/dnstap/golang-dnstap"
	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"
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

// Inserter gerencia o batch insert no ClickHouse
type Inserter struct {
	conn          clickhouse.Conn
	serverID      string
	batchSize     int
	flushInterval time.Duration
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

	for {
		select {
		case <-ctx.Done():
			if len(batch) > 0 {
				ins.flush(batch)
			}
			return

		case frame := <-msgCh:
			row, err := parseFrame(frame, ins.serverID)
			if err != nil {
				continue
			}
			batch = append(batch, row)
			if len(batch) >= ins.batchSize {
				ins.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				ins.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush envia o batch para o ClickHouse
func (ins *Inserter) flush(rows []DNSRow) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	b, err := ins.conn.PrepareBatch(ctx,
		`INSERT INTO dns_telemetry.dns_queries
		 (ts, server_id, message_type, client_ip, client_port, protocol,
		  qname, qtype, qclass, rcode, answer_count, latency_us,
		  from_cache, response_size, do_flag, ad_flag)`)
	if err != nil {
		log.Printf("[inserter] PrepareBatch erro: %v", err)
		return
	}

	for _, r := range rows {
		if err := b.Append(
			r.Ts, r.ServerID, r.MessageType, r.ClientIP, r.ClientPort, r.Protocol,
			r.Qname, r.Qtype, r.Qclass, r.Rcode, r.AnswerCount, r.LatencyUs,
			r.FromCache, r.ResponseSize, r.DoFlag, r.AdFlag,
		); err != nil {
			log.Printf("[inserter] Append erro: %v", err)
		}
	}

	if err := b.Send(); err != nil {
		log.Printf("[inserter] Send erro (batch=%d): %v", len(rows), err)
		return
	}

	log.Printf("[inserter] flush OK | %d rows", len(rows))
}

// parseFrame converte um frame dnstap protobuf em DNSRow
func parseFrame(frame []byte, serverID string) (DNSRow, error) {
	msg := &dnstap.Dnstap{}
	if err := proto.Unmarshal(frame, msg); err != nil {
		return DNSRow{}, fmt.Errorf("unmarshal: %w", err)
	}

	m := msg.GetMessage()
	if m == nil {
		return DNSRow{}, fmt.Errorf("mensagem dnstap vazia")
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

	if len(wireMsg) > 0 {
		dnsMsg := new(dns.Msg)
		if err := dnsMsg.Unpack(wireMsg); err == nil {
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

				// Detecta cache hit: AA=false, RA=true, latencia muito baixa
				// ou via flag TC (truncated indica resolucao local)
				if dnsMsg.RecursionAvailable && !dnsMsg.Authoritative {
					row.FromCache = 0
				}
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

	// Latencia: diferenca entre query e response time (microsegundos)
	qSec := m.GetQueryTimeSec()
	qNsec := m.GetQueryTimeNsec()
	rSec := m.GetResponseTimeSec()
	rNsec := m.GetResponseTimeNsec()
	if qSec > 0 && rSec > 0 && rSec >= qSec {
		diffNs := int64(rSec-qSec)*1e9 + int64(rNsec) - int64(qNsec)
		if diffNs > 0 {
			row.LatencyUs = uint32(diffNs / 1000)
		}
	}

	// Normaliza qname: remove trailing dot para consistencia
	row.Qname = strings.TrimSuffix(row.Qname, ".")

	return row, nil
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
