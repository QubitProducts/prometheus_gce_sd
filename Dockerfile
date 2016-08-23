FROM ubuntu

COPY ./prometheus_gce_sd /usr/bin/prometheus_gce_sd

ENTRYPOINT ["/usr/bin/prometheus_gce_sd"]
