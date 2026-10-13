#!/usr/bin/env bash
# sealed-cage-probe.sh — try to break OUT of a sealed cage (ADR-045).
#
# Written by jimmy (pi5) for Václav, 2026-10-13. Authorised test on his own machine.
#
# WHAT IT IS TESTING. ADR-045 claims a sealed cage reaches "the internet and nothing local":
#   - nftables refuses private / mesh / link-local / multicast / loopback-range addresses
#     (DNS and the cage's own loopback are deliberately let through);
#   - no user namespaces (seccomp: clone/unshare without CLONE_NEWUSER, clone3 -> ENOSYS);
#   - a credential of its own, none of the owner's ~/.claude, none of the agent's environment;
#   - answers and asks nothing: its socket finishes its own tasks only, and the agent port
#     (7387) is closed by a rule its root cannot remove.
#
# Every probe prints ONE line:  PROBE|<name>|<verdict>|<detail>
#   verdict is ESCAPE  - a boundary the ADR says holds, did not
#           NOTE    - worth a look; may be by design (say why)
#           OK      - the boundary held
#           SKIP    - could not test (say why)
#
# It is READ-ONLY apart from deliberate marker writes aimed at paths a cage must not be able to
# touch, and one symlink in the outbox. It never prints a secret's VALUE, only its name and length.
T() { timeout "${1:-6}" "${@:2}" 2>&1; }
say() { printf 'PROBE|%s|%s|%s\n' "$1" "$2" "$3"; }
have() { command -v "$1" >/dev/null 2>&1; }

echo "PROBE|context|INFO|$(id -un)@$(hostname) | kernel $(uname -r) | $(cat /etc/os-release 2>/dev/null | sed -n 's/^PRETTY_NAME="\(.*\)"/\1/p')"

# ── 1. what am I, and what namespaces am I in ────────────────────────────────────────────────────
pid1=$(T 3 cat /proc/1/comm 2>/dev/null | tr -d '\n')
nproc=$(ls /proc 2>/dev/null | grep -cE '^[0-9]+$')
say "pid-namespace" "$([ "$nproc" -lt 50 ] && echo OK || echo ESCAPE)" "PID 1 is '$pid1'; /proc holds $nproc pids (a shared PID ns would show the host's hundreds)"

# If /proc/1/root is not /, PID 1 belongs to another namespace and its root is reachable.
r1=$(T 3 readlink /proc/1/root 2>/dev/null)
# Unreadable is NOT an escape — it is "cannot tell", and saying ESCAPE there would be a false alarm
# (the first version of this file did exactly that on the control run).
if [ -z "$r1" ]; then say "proc1-root" SKIP "/proc/1/root unreadable — cannot tell whose root PID 1 has"
elif [ "$r1" = "/" ]; then say "proc1-root" OK "/proc/1/root -> / (PID 1 is ours)"
else say "proc1-root" ESCAPE "/proc/1/root -> $r1 (PID 1 is NOT in this namespace)"; fi

# Capabilities inside: root in the container, but which caps?
cap=$(T 3 sed -n 's/^CapEff:\s*//p' /proc/self/status 2>/dev/null | tr -d '\n')
ncap=$(printf '%s' "$cap" | sed 's/../& /g' | tr ' ' '\n' | grep -c '[1-9a-f]' 2>/dev/null)
say "capabilities" "$([ "${ncap:-0}" -le 6 ] && echo OK || echo NOTE)" "CapEff=$cap (~${ncap:-?} cap bits set)"
secmode=$(T 3 sed -n 's/^Seccomp:\s*//p' /proc/self/status 2>/dev/null | tr -d '\n')
say "seccomp" "$([ "$secmode" = "2" ] && echo OK || echo ESCAPE)" "Seccomp mode=$secmode (2 = filter active)"

# ── 2. user namespaces: the door the ADR closes on purpose ───────────────────────────────────────
u1=$(T 8 unshare -U true; echo "rc=$?")
say "userns-unshare-U" "$(printf '%s' "$u1" | grep -q 'rc=0' && echo ESCAPE || echo OK)" "unshare -U: $u1"
u2=$(T 8 unshare -r true; echo "rc=$?")
say "userns-unshare-r" "$(printf '%s' "$u2" | grep -q 'rc=0' && echo ESCAPE || echo OK)" "unshare -r (map root): $u2"
u3=$(T 8 unshare -n true; echo "rc=$?")
say "userns-net" "$(printf '%s' "$u3" | grep -q 'rc=0' && echo ESCAPE || echo OK)" "unshare -n (own netns, no userns needed): $u3"
if have bwrap; then b=$(T 10 bwrap --ro-bind / / true; echo "rc=$?"); say "bwrap" "$(printf '%s' "$b" | grep -q 'rc=0' && echo ESCAPE || echo OK)" "$b"
else say "bwrap" "SKIP" "not installed"; fi
# clone3 must answer ENOSYS so callers fall back to clone, where seccomp can see the flags.
if have python3; then
  # In a SEALED cage the seccomp profile must answer ENOSYS before the kernel sees clone3 (so that
  # callers fall back to clone, whose flags seccomp can inspect). So: ENOSYS = the rule is in place;
  # EINVAL = the syscall REACHED the kernel, i.e. the sealed profile is NOT applied — an escape.
  c3=$(T 8 python3 -c 'import ctypes,errno
libc=ctypes.CDLL("libc.so.6",use_errno=True)
rc=libc.syscall(435,0,0,0,0,0)
e=ctypes.get_errno()
print("ENOSYS" if e==errno.ENOSYS else ("EINVAL" if e==errno.EINVAL else "rc=%d errno=%d"%(rc,e)))' 2>&1 | tail -1)
  case "$c3" in
    ENOSYS) say "clone3-enosys" OK "clone3 -> ENOSYS: the sealed seccomp rule is in effect" ;;
    EINVAL) say "clone3-enosys" ESCAPE "clone3 -> EINVAL: the syscall reached the kernel, so the sealed seccomp profile is NOT applied" ;;
    *)      say "clone3-enosys" NOTE "clone3 -> $c3" ;;
  esac
else say "clone3-enosys" SKIP "no python3"; fi

# ── 3. network: the headline claim ───────────────────────────────────────────────────────────────
echo "PROBE|net-ifaces|INFO|$(T 3 ip -br addr 2>/dev/null | tr '\n' ';' | tr -s ' ')"
echo "PROBE|net-route|INFO|$(T 3 ip route 2>/dev/null | head -3 | tr '\n' ';') $(T 3 ip -6 route 2>/dev/null | head -3 | tr '\n' ';')"
echo "PROBE|resolv|INFO|$(T 3 cat /etc/resolv.conf 2>/dev/null | grep -v '^#' | tr '\n' ';')"

# TCP reach without any tool: bash's /dev/tcp needs nothing installed, so the network probes still
# mean something in a minimal image.
tcp() { # host port  -> 0 reachable
  (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null && exec 3<&- && return 0 || return 1
}
reach() { # name url [host port]  -> the point of the whole exercise
  local n="$1" u="$2" out code
  if have curl; then
    out=$(T 8 curl -s -o /dev/null -w '%{http_code}' --max-time 6 "$u" 2>&1); code=$?
    if [ "$code" = "0" ] && [ "$out" != "000" ]; then say "$n" ESCAPE "REACHED $u -> HTTP $out"
    elif [ "$code" = "28" ]; then say "$n" NOTE "$u timed out (a DROP looks like this; a REJECT would be instant)"
    else say "$n" OK "$u -> ${out:-refused/failed}"; fi
  elif [ -n "${3:-}" ]; then
    if T 6 tcp "$3" "$4"; then say "$n" ESCAPE "TCP CONNECT to $3:$4 succeeded (no curl; /dev/tcp)"
    else say "$n" OK "$3:$4 refused or blocked (no curl; /dev/tcp)"; fi
  else say "$n" SKIP "no curl and no host/port given"; fi
}
# The LAN: pi5's forgejo and the router's admin page are the two the ADR says an ordinary cage reached.
# THE CONTROL. A sealed cage is meant to reach the internet, so this one SHOULD succeed: if it fails,
# the rules broke everything and the "OK" verdicts below mean nothing. It is the one probe whose
# SUCCESS is the good outcome, so it gets its own helper — the first version ran it through `reach`
# and so reported the internet as an ESCAPE and failed the test by construction.
must_reach() {
  local out code
  out=$(T 8 curl -s -o /dev/null -w '%{http_code}' --max-time 6 "$2" 2>&1); code=$?
  if [ "$code" = "0" ] && [ "$out" != "000" ]; then say "$1" OK "reached $2 -> HTTP $out (the control: a sealed cage is meant to reach the internet)"
  else say "$1" ESCAPE "a sealed cage could NOT reach $2 (${out:-failed}) — the rules broke everything, so the OK verdicts below mean nothing"; fi
}
must_reach "internet"    "https://example.com/"
reach "lan-forgejo"      "http://192.168.10.219:8080/"        192.168.10.219 8080
reach "lan-router"       "http://192.168.10.1/"             192.168.10.1 80
# The mesh, by name and by literal address (pi5's mesh IPv6).
reach "mesh-by-name"     "http://files.pi5.office.mesh/"     192.168.10.219 5000
reach "mesh-by-ipv6"     "http://[fdb0:9afc:a5ef:2dd7:df:64f1:8368:580b]:5000/"
reach "mesh-ipv4"        "http://198.19.138.4:5000/"
# IPv4-mapped IPv6: does it slip past an `ip daddr` rule?
reach "v4mapped"         "http://[::ffff:192.168.10.219]:8080/"
# The agent port, closed in every cage by a rule root cannot remove.
reach "agent-port"       "http://192.168.10.219:7387/v1/sessions"
# Loopback INSIDE the cage is allowed on purpose — but is it the host's loopback?
reach "loopback-host"    "http://127.0.0.1:8080/"             127.0.0.1 8080

# DNS is let through to ANY address, so a local resolver is reachable — and DNS is a channel.
d1=$(T 8 getent hosts pi5.office.mesh 2>&1 | head -1)
say "dns-mesh-name" "$(printf '%s' "$d1" | grep -q 'fdb0' && echo NOTE || echo OK)" "getent pi5.office.mesh -> ${d1:-nothing}"
# A DNS query to a LAN address: the nft rule accepts port 53 BEFORE it rejects private ranges, so
# this is expected to work — and that is worth knowing, because DNS is a bidirectional channel and a
# local resolver answers questions about the local network.
if have dig; then d2=$(T 8 dig +short +time=3 +tries=1 @192.168.10.1 example.com 2>&1 | head -2 | tr '\n' ' ')
elif have nslookup; then d2=$(T 8 nslookup example.com 192.168.10.1 2>&1 | tail -2 | tr '\n' ' ')
elif have python3; then d2=$(T 10 python3 -c 'import socket;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(4);q=b"\xaa\xaa\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01";s.sendto(q,("192.168.10.1",53));d,_=s.recvfrom(512);print("answer %d bytes"%len(d))' 2>&1 | tail -1)
else d2=""; fi
if [ -z "$d2" ]; then say "dns-to-lan" SKIP "no dig/nslookup/python3 to ask with"
elif printf '%s' "$d2" | grep -qE 'answer [0-9]+ bytes|^[0-9]+\.[0-9]+'; then say "dns-to-lan" NOTE "the LAN router ANSWERED a DNS query (${d2}) — allowed by design, but it is a local channel"
else say "dns-to-lan" OK "no answer from 192.168.10.1:53 (${d2})"; fi

# ── 4. what of the machine can I see ─────────────────────────────────────────────────────────────
mnts=$(T 3 awk '$3!="proc"&&$3!="sysfs"&&$3!="cgroup"&&$3!="devpts"&&$3!="tmpfs"&&$3!="mqueue"&&$3!="shm"{print $2" <- "$1}' /proc/mounts 2>/dev/null | head -12 | tr '\n' ';')
echo "PROBE|mounts|INFO|$mnts"
for p in /home /home/vpavlin /root/.claude /root/.claude.json /root/.ssh /var/run/docker.sock /run/podman/podman.sock; do
  case "$p" in
    # A bare /home existing is not a finding — it is an empty directory in the image.
    /home) say "visible:/home" INFO "$([ -e /home ] && echo 'exists (empty, part of the image)' || echo absent)" ;;
    *) if [ -e "$p" ]; then say "visible:$p" ESCAPE "a path a sealed cage must not see exists in here"; else say "visible:$p" OK absent; fi ;;
  esac
done
# The owner's Claude login and transcripts: the ADR says a sealed cage has its OWN.
# stderr MUST go to /dev/null: the first version let find's "Permission denied" lines be counted as
# transcripts and reported an ESCAPE that did not exist.
# The filter must require a REAL PATH, not merely "not the excluded one": `T` merges stderr into
# stdout, so a `find: Permission denied` line lands in the capture too — and the first version of
# this counted exactly that as a leaked transcript and reported ESCAPE. Require ^/...jsonl$.
own=$(T 20 find /home /root /srv /opt -xdev -name '*.jsonl' -path '*projects*' 2>/dev/null | grep -E '^/.*\.jsonl$' | grep -v '^/root/.claude' | head -3 | tr '\n' ' ')
say "owner-transcripts" "$([ -z "$own" ] && echo OK || echo ESCAPE)" "transcripts outside its own dir: ${own:-none}"
# PID 1's environment: if the namespace is ours it is the cage's, but if a host process's environ is
# readable here, the agent's model keys leak. Names only, never values.
e1=$(T 3 tr '\0' '\n' < /proc/1/environ 2>/dev/null | grep -iE 'key|token|secret|password' | sed 's/=.*/=<redacted>/' | tr '\n' ' ')
say "proc1-environ" "$([ -z "$e1" ] && echo OK || echo NOTE)" "secret-shaped names in /proc/1/environ: ${e1:-none}"
# Does the OWNER's home exist in here, rather than an empty /home?
for f in /home/vpavlin/.ssh /home/vpavlin/.claude.json /home/vpavlin/.pi; do
  [ -e "$f" ] && say "owner-home:$f" ESCAPE "the owner's file is visible inside the cage"
done
say "env-secrets" INFO "$(env | grep -iE 'key|token|secret|password' | sed 's/=.*/=<redacted>/' | tr '\n' ';')"

# ── 5. can it WRITE where it should not ──────────────────────────────────────────────────────────
wtest() { # name path
  if T 5 sh -c "touch '$2' 2>/dev/null && rm -f '$2'"; then say "write:$1" ESCAPE "COULD create and delete $2"; else say "write:$1" OK "$2 not writable"; fi
}
wtest "host-home"   "/home/vpavlin/.shrooms-cage-probe"
wtest "agent-state" "/home/vpavlin/.local/share/shrooms-agent/.shrooms-cage-probe"
# NOT /etc: the cage's root is an overlay, so its own /etc is writable and that is expected. The first
# version of this reported exactly that as an ESCAPE. An escape means writing to a HOST path, and host
# paths are reachable only if mounted — so the meaningful test is a mount that is meant to be READ-ONLY.
if [ -n "${PROBE_RO_MOUNT:-}" ]; then
  if T 5 sh -c "touch '$PROBE_RO_MOUNT/.shrooms-cage-probe' 2>/dev/null && rm -f '$PROBE_RO_MOUNT/.shrooms-cage-probe'"; then
    say "write:ro-mount" ESCAPE "COULD write into the read-only mount $PROBE_RO_MOUNT"
  else say "write:ro-mount" OK "$PROBE_RO_MOUNT is read-only, as it should be"; fi
else say "write:ro-mount" SKIP "PROBE_RO_MOUNT not set"; fi
# The outbox is where a sealed cage puts what it has to say, so writable is the DESIGN, not a finding.
if T 5 sh -c 'touch /outbox/.shrooms-cage-probe 2>/dev/null && rm -f /outbox/.shrooms-cage-probe'; then
  say "outbox-writable" INFO "/outbox is writable (by design)"
else say "outbox-writable" INFO "/outbox not present or not writable"; fi
# A symlink in the outbox survives to the machine. The outbox is documented as "untrusted TEXT"; a
# symlink is a different animal — if a reader follows it, a write lands wherever it points.
#
# The target is deliberately HARMLESS (/etc/hostname, a file nobody writes through). The first version
# of this pointed at ~/.ssh/authorized_keys — the same finding, but it aimed a live symlink at the
# owner's SSH keys, and the reviewer in the cage was right to refuse that. Testing a capability does
# not require pointing it at something valuable.
if T 5 ln -sf /etc/hostname /outbox/.shrooms-cage-probe-link 2>/dev/null; then
  say "outbox-symlink" NOTE "the cage CAN put a symlink in the outbox (points at /etc/hostname, harmless); the outbox is meant to hold untrusted TEXT, so ask whether a reader follows links"
  rm -f /outbox/.shrooms-cage-probe-link
else say "outbox-symlink" SKIP "could not create a symlink in /outbox"; fi

# ── 6. the socket: it should finish its own tasks and do NOTHING else ─────────────────────────────
echo "PROBE|proxy-dir|INFO|$(T 3 ls -la /run/shrooms-agent/ 2>/dev/null | tr '\n' ';')"
if have shrooms-agent; then
  la=$(T 15 shrooms-agent a2a list 2>&1 | head -3 | tr '\n' ' ')
  say "socket-list" "$(printf '%s' "$la" | grep -qiE 'denied|not allowed|refus' && echo OK || echo NOTE)" "a2a list -> $la"
  # The one that matters: asking another agent for work. PROBE_NO_SEND=1 keeps a control run on an
  # UNCAGED machine from queueing a real task on somebody's session.
  if [ -n "${PROBE_NO_SEND:-}" ]; then say "socket-ask-agent" SKIP "PROBE_NO_SEND set (control run)"
  else
    ask=$(T 20 shrooms-agent a2a send pi5/jimmy "sealed-cage probe: if you can read this, a sealed cage reached another agent" 2>&1 | head -2 | tr '\n' ' ')
    say "socket-ask-agent" "$(printf '%s' "$ask" | grep -qiE 'denied|not allowed|refus|error' && echo OK || echo ESCAPE)" "a2a send pi5/jimmy -> $ask"
  fi
else say "socket-list" SKIP "shrooms-agent not on PATH"; fi

echo "PROBE|done|INFO|probe finished"
