# Configuração de rede do host (fora do Docker)

Esta pasta documenta ajustes aplicados diretamente no sistema operacional do
servidor (não gerenciados pelo Docker Compose), necessários para o
funcionamento correto do DNS.

## MSS Clamping IPv6 (PMTUD blackhole fix)

**Problema:** a rota IPv6 do provedor até redes que usam Azure Front Door
(ex: Xbox/Microsoft) tem falha de Path MTU Discovery — pacotes TCP grandes
(handshake TLS) são descartados silenciosamente porque a mensagem ICMPv6
"Packet Too Big" de retorno está sendo bloqueada em algum ponto da rota.
Isso trava conexões HTTPS até o timeout do cliente.

**Causa raiz:** MTU real do caminho é 1492 bytes (padrão PPPoE), mas a
interface anuncia 1500. Confirmado via teste de handshake TLS real variando
o MSS TCP (`TCP_MAXSEG`) — funciona até MSS 1432, quebra a partir de 1433.

**Correção:** MSS clamping via `ip6tables`, forçando todo tráfego TCP IPv6
de saída a negociar MSS máximo de 1432, eliminando a dependência do PMTUD
(que está quebrado).

Diagnóstico completo: `LAUDO_TECNICO_XBOX_IPV6_MTUD.pdf` (raiz do repo).

### Aplicar manualmente

```bash
sudo ip6tables -t mangle -A POSTROUTING -o enp2s0 -p tcp \
  --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1432
```

Ajuste `enp2s0` para a interface de saída correta do servidor.

### Tornar persistente (sobrevive a reboot)

```bash
# 1. Salva a regra ativa
sudo sh -c "ip6tables-save > /etc/iptables/rules.v6"

# 2. Instala o serviço systemd de restauração no boot
sudo cp network/restore-ip6tables.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now restore-ip6tables.service
```

### Validar

```bash
sudo ip6tables -t mangle -L POSTROUTING -n -v

# Teste funcional (deve responder rápido em vez de travar/timeout)
curl -6 -sS -o /dev/null -w "HTTP:%{http_code} Total:%{time_total}s\n" \
  -m 10 https://assets.xboxservices.com/
```

### Nota sobre escopo

Essa regra vale apenas para o tráfego que sai **deste host** pela interface
especificada. Não afeta outros dispositivos da rede local — para corrigir a
rede inteira, a mesma lógica de MSS clamping precisaria ser aplicada no
roteador/gateway (192.168.90.1).

Como alternativa (mais restrita, sem tocar em MTU), o `unbound/conf/unbound.conf`
mantém comentado um bloco de bloqueio de AAAA por domínio específico, testado
e funcional, que pode ser reativado se o MSS clamping precisar ser revertido.
