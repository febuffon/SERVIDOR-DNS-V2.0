# Zabbix Agent2 + unbound-control (para coletar stats do Unbound via
# UserParameter) + docker-cli (para healthcheck/discovery de containers).
# Base Alpine, mesma familia da imagem oficial.
FROM zabbix/zabbix-agent2:alpine-7.0-latest

USER root
RUN apk add --no-cache unbound docker-cli
COPY scripts /etc/zabbix/scripts
RUN chmod +x /etc/zabbix/scripts/*.sh
USER zabbix
