#!/bin/sh
set -eu

usage() {
  echo "usage: $0 install|upgrade|uninstall /path/to/yufeng-edge" >&2
  exit 2
}

[ "$#" -eq 2 ] || usage
[ "$(id -u)" -eq 0 ] || { echo "install-linux.sh must run as root" >&2; exit 1; }
action=$1
binary=$2
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

case "$action" in
  install)
    test -x "$binary"
    id yufeng >/dev/null 2>&1 || useradd --system --home /var/lib/yufeng --shell /usr/sbin/nologin yufeng
    install -d -o yufeng -g yufeng -m 0750 /var/lib/yufeng/edge /var/lib/yufeng/telemetry /run/yufeng
    install -d -o root -g yufeng -m 0750 /etc/yufeng/credentials /etc/yufeng/trust /etc/yufeng/tls
    install -m 0755 "$binary" /usr/local/bin/yufeng-edge
    install -m 0644 "$script_dir/yufeng-edge.service" /etc/systemd/system/yufeng-edge.service
    if [ ! -f /etc/yufeng/edge.env ]; then
      install -m 0640 -o root -g yufeng "$script_dir/edge.env.example" /etc/yufeng/edge.env
      echo "edit /etc/yufeng/edge.env and provision /etc/yufeng/{credentials,trust,tls} before starting" >&2
    fi
    systemctl daemon-reload
    systemctl enable yufeng-edge.service
    ;;
  upgrade)
    test -x "$binary"
    test -f /etc/systemd/system/yufeng-edge.service
    install -m 0755 "$binary" /usr/local/bin/yufeng-edge
    install -m 0644 "$script_dir/yufeng-edge.service" /etc/systemd/system/yufeng-edge.service
    systemctl daemon-reload
    systemctl restart yufeng-edge.service
    ;;
  uninstall)
    systemctl disable --now yufeng-edge.service 2>/dev/null || true
    rm -f /etc/systemd/system/yufeng-edge.service /usr/local/bin/yufeng-edge
    systemctl daemon-reload
    echo "configuration and state under /etc/yufeng and /var/lib/yufeng were preserved" >&2
    ;;
  *) usage ;;
esac
