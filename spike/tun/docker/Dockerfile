FROM ubuntu:26.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus dbus-bin \
      iproute2 iputils-ping ca-certificates curl \
      dnsmasq openssl python3 procps less kmod gcc libc6-dev \
 && rm -rf /var/lib/apt/lists/*

# A container has no getty/console; drop the units that only add failed noise.
RUN rm -f /etc/systemd/system/getty.target.wants/getty@tty1.service \
          /etc/systemd/system/multi-user.target.wants/getty.target \
          /etc/systemd/system/sysinit.target.wants/systemd-udevd.service

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
