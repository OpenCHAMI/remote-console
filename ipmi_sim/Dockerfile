FROM alpine

RUN apk --update --no-cache add openipmi-lanserv socat tini

RUN mkdir -p /tmp/chassis

COPY lan.conf /ipmi_sim/lan.conf
COPY sim.emu /ipmi_sim/sim.emu
COPY entrypoint.sh /ipmi_sim/entrypoint.sh

EXPOSE 623/udp

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/ipmi_sim/entrypoint.sh"]