package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	fs "github.com/farsightsec/golang-framestream"
	dnstap "github.com/dnstap/golang-dnstap"
	"google.golang.org/protobuf/proto"
)

// content type negociado pelo protocolo fstrm do dnstap
var dnstapContentType = []byte("protobuf:dnstap.Dnstap")

func main() {
	socketPath := getEnv("DNSTAP_SOCKET", "/var/dnstap/dnstap.sock")
	chDSN      := getEnv("CLICKHOUSE_DSN", "clickhouse://admin:changeme@localhost:9000/dns_telemetry")
	serverID   := getEnv("SERVER_ID", "dns-cgr01")

	batchSize     := envInt("BATCH_SIZE", 500)
	flushInterval := time.Duration(envInt("FLUSH_INTERVAL_MS", 1000)) * time.Millisecond

	log.Printf("[collector] iniciando | socket=%s server_id=%s batch=%d flush=%s",
		socketPath, serverID, batchSize, flushInterval)

	msgCh := make(chan []byte, 8192)

	ins, err := NewInserter(chDSN, serverID, batchSize, flushInterval)
	if err != nil {
		log.Fatalf("[collector] falha ao conectar ClickHouse: %v", err)
	}
	defer ins.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go handleSignals(cancel)
	go ins.Run(ctx, msgCh)
	listenSocket(ctx, socketPath, msgCh, ins)

	log.Println("[collector] encerrado")
}

func listenSocket(ctx context.Context, socketPath string, msgCh chan<- []byte, ins *Inserter) {
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("[collector] erro ao criar socket %s: %v", socketPath, err)
	}
	defer func() {
		ln.Close()
		os.Remove(socketPath)
	}()

	if err := os.Chmod(socketPath, 0777); err != nil {
		log.Printf("[collector] aviso chmod socket: %v", err)
	}

	log.Printf("[collector] aguardando Unbound em %s", socketPath)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[collector] accept erro: %v — retry em 2s", err)
				time.Sleep(2 * time.Second)
				continue
			}
		}
		log.Println("[collector] Unbound conectado")
		go handleConn(ctx, conn, msgCh, ins)
	}
}

func handleConn(ctx context.Context, conn net.Conn, msgCh chan<- []byte, ins *Inserter) {
	defer func() {
		conn.Close()
		log.Println("[collector] conexao encerrada")
	}()

	// Decodificador fstrm em modo bidirecional (handshake com o Unbound)
	dec, err := fs.NewDecoder(conn, &fs.DecoderOptions{
		ContentType:   dnstapContentType,
		Bidirectional: true,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		log.Printf("[collector] fstrm decoder erro: %v", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		frame, err := dec.Decode()
		if err != nil {
			log.Printf("[collector] fstrm decode erro: %v", err)
			return
		}
		if len(frame) == 0 {
			continue
		}

		// Valida protobuf antes de enfileirar
		msg := &dnstap.Dnstap{}
		if err := proto.Unmarshal(frame, msg); err != nil {
			continue
		}

		// Copia o frame (dec reutiliza o buffer)
		buf := make([]byte, len(frame))
		copy(buf, frame)

		select {
		case msgCh <- buf:
		default:
			ins.RecordDroppedFrame()
			log.Println("[collector] canal cheio, frame descartado")
		}
	}
}

func handleSignals(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Println("[collector] sinal recebido, encerrando...")
	cancel()
}
