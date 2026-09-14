#!/bin/bash
# Gather everything needed to explain a failed connection, in one pass.
#
# Both on-device failures so far were diagnosed from logs belonging to something
# other than this app -- the kernel audit log, and NetworkManager's journal --
# while the app's own logs were silent. So collect all of them together.
set -uo pipefail
PKG=ocvpn.franzthiemann
RT=/run/user/32011/$PKG
DD=/home/phablet/.local/share/$PKG

adb get-state >/dev/null 2>&1 || { echo "no device on adb" >&2; exit 1; }

adb shell "
echo '=========== tunnel state ==========='
for d in $RT $DD/run; do
  [ -f \$d/status.json ] && { echo \"-- \$d\"; cat \$d/status.json; }
done
echo
echo '=========== processes ==========='
pgrep -af 'bin/openconnect|bin/ocbridge|bin/openvpn' | grep -v pgrep
echo
echo '=========== listening sockets ==========='
ss -ltnp 2>/dev/null | grep 1194 || echo '  nothing on tcp/1194'
ss -lunp 2>/dev/null | grep 1194 || echo '  nothing on udp/1194'
echo
echo '=========== our openvpn server log ==========='
for d in $RT $DD/run; do [ -f \$d/openvpn.log ] && tail -20 \$d/openvpn.log; done
echo
echo '=========== our openconnect log ==========='
for d in $RT $DD/run; do [ -f \$d/openconnect.log ] && tail -10 \$d/openconnect.log; done
echo
echo '=========== NetworkManager client side ==========='
journalctl --system --no-pager --since '-10min' 2>/dev/null | grep -iE 'nm-openvpn|vpn\[' | tail -25
echo
echo '=========== AppArmor denials (kernel log) ==========='
dmesg 2>/dev/null | grep DENIED | grep $PKG | grep -viE 'sys/devices|sys/kernel|dist-packages|loginuid|qtshadercache|ubuntu-touch-session' | tail -15
echo
echo '=========== the NM profile ==========='
nmcli -f vpn.data,vpn.service-type connection show '127.0.0.1' 2>/dev/null | head -5
" 2>&1 | tr -d '\r'
