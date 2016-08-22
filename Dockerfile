#FROM		quay.io/prometheus/busybox:latest
FROM		ubuntu
MAINTAINER	Calum Gardner <calum@qubit.com>

RUN			apt-get update
RUN			apt-get install -y ca-certificates

COPY		./gce-discoverer	/gce-discoverer
COPY		./run.sh			/run.sh

EXPOSE		80
WORKDIR		/

ENTRYPOINT	["/run.sh"]
