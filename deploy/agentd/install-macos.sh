#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 https://brain:9050 worker-id /path/brain-ca.pem" >&2
  exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "install-macos.sh must run as root" >&2
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
install_root="/Library/Application Support/YuFeng"
state_dir="$install_root/agentd"
mkdir -p "$install_root/bin" "$state_dir"
chmod 700 "$state_dir"
install -m 0755 "$package_dir/bin/yufeng-agentd" "$package_dir/bin/yufeng-run" "$install_root/bin/"
install -m 0644 "$tls_ca" "$install_root/brain-ca.pem"
if [ ! -s "$state_dir/worker-refresh" ] && [ ! -s "$state_dir/enrollment.json" ]; then
  "$install_root/bin/yufeng-agentd" -enroll "-brain=$brain" "-worker=$worker" \
    "-tls-ca=$install_root/brain-ca.pem" "-state-dir=$state_dir"
fi
if [ ! -s "$state_dir/worker-refresh" ] && [ ! -s "$state_dir/enrollment.json" ]; then
  echo "worker enrollment did not create persistent state" >&2
  exit 1
fi

cat >/Library/LaunchDaemons/com.yufeng.agentd.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.yufeng.agentd</string>
  <key>ProgramArguments</key><array>
    <string>$install_root/bin/yufeng-agentd</string>
    <string>-brain=$brain</string><string>-worker=$worker</string>
    <string>-tls-ca=$install_root/brain-ca.pem</string>
    <string>-state-dir=$state_dir</string>
    <string>-activate</string>
  </array>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/var/log/yufeng-agentd.log</string>
  <key>StandardErrorPath</key><string>/var/log/yufeng-agentd.log</string>
</dict></plist>
EOF
chmod 0644 /Library/LaunchDaemons/com.yufeng.agentd.plist
launchctl bootout system/com.yufeng.agentd >/dev/null 2>&1 || true
launchctl bootstrap system /Library/LaunchDaemons/com.yufeng.agentd.plist
