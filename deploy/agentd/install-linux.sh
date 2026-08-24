#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 https://brain:9050 worker-id /path/brain-ca.pem" >&2
  exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "install-linux.sh must run as root" >&2
  exit 1
fi

brain=$1
worker=$2
tls_ca=$3
case "$brain" in https://*) ;; *) echo "brain must use https" >&2; exit 1 ;; esac
case "$brain" in *[!A-Za-z0-9._:/-]*) echo "brain URL contains unsupported characters" >&2; exit 1 ;; esac
case "$worker" in *[!A-Za-z0-9._-]*|'') echo "worker id contains unsupported characters" >&2; exit 1 ;; esac
test -f "$tls_ca"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
package_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
test -x "$package_dir/bin/yufeng-agentd"
test -x "$package_dir/bin/yufeng-run"

install -d -m 0755 /opt/yufeng/bin /etc/yufeng
install -d -m 0700 /var/lib/yufeng/agentd
install -m 0755 "$package_dir/bin/yufeng-agentd" "$package_dir/bin/yufeng-run" /opt/yufeng/bin/
install -m 0644 "$tls_ca" /etc/yufeng/brain-ca.pem
if [ ! -s /var/lib/yufeng/agentd/worker-refresh ] && [ ! -s /var/lib/yufeng/agentd/enrollment.json ]; then
  /opt/yufeng/bin/yufeng-agentd -enroll "-brain=$brain" "-worker=$worker" \
    -tls-ca=/etc/yufeng/brain-ca.pem -state-dir=/var/lib/yufeng/agentd
fi
if [ ! -s /var/lib/yufeng/agentd/worker-refresh ] && [ ! -s /var/lib/yufeng/agentd/enrollment.json ]; then
  echo "worker enrollment did not create persistent state" >&2
  exit 1
fi

cat >/etc/systemd/system/yufeng-agentd.service <<EOF
[Unit]
Description=YuFeng run supervisor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/yufeng/bin/yufeng-agentd -brain=$brain -worker=$worker -tls-ca=/etc/yufeng/brain-ca.pem -state-dir=/var/lib/yufeng/agentd -activate
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/yufeng/agentd
ProtectHome=yes
RestrictSUIDSGID=yes
LockPersonality=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable yufeng-agentd.service
systemctl restart yufeng-agentd.service
