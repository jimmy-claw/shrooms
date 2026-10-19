#!/usr/bin/env python3
"""Writes grafana/dashboards/shrooms-traffic.json (docs/metrics.md).

Generated rather than hand-edited JSON, so the queries stay readable here and
consistent with each other: one definition of "a physical interface".
"""
import json, pathlib

# Interfaces that are not the machine's own wire: the mesh's tunnels, container
# and VM bridges. What is left is what a router sees.
VIRTUAL = '(logos|shrooms|docker|veth|br-|virbr|podman|cni|vnet|tun|tap|wg|flannel|lxc|tailscale).*'
PHYS = f'interface!~"{VIRTUAL}"'

panels, y = [], 0

def row(title):
    global y
    panels.append({"type": "row", "title": title, "collapsed": False, "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}, "panels": []})
    y += 1

def panel(kind, title, targets, w, h, x=0, unit="Bps", desc="", extra=None):
    p = {"type": kind, "title": title, "description": desc, "datasource": {"type": "prometheus", "uid": "prometheus"},
         "gridPos": {"h": h, "w": w, "x": x, "y": y},
         "fieldConfig": {"defaults": {"unit": unit}, "overrides": []},
         "targets": [{"refId": chr(65 + i), "expr": e, "legendFormat": l, "datasource": {"type": "prometheus", "uid": "prometheus"}}
                     for i, (e, l) in enumerate(targets)]}
    if kind == "bargauge":
        p["options"] = {"orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True,
                        "reduceOptions": {"calcs": ["lastNotNull"], "values": False}}
    if kind == "stat":
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "values": False}, "colorMode": "value", "graphMode": "none"}
    if kind == "timeseries":
        p["fieldConfig"]["defaults"]["custom"] = {"fillOpacity": 10, "lineWidth": 1}
    if extra:
        p.update(extra)
    panels.append(p)

def advance(h):
    global y
    y += h

row("Machines — what the router sees (physical interfaces)")
panel("timeseries", "Traffic per machine",
      [(f'sum by (node) (rate(shrooms_host_receive_bytes_total{{{PHYS}}}[5m]))', "{{node}} in"),
       (f'-sum by (node) (rate(shrooms_host_transmit_bytes_total{{{PHYS}}}[5m]))', "{{node}} out")],
      16, 9, desc="Every physical interface of each machine, in (up) and out (down). The sum over machines on one LAN is roughly what its router counts.")
panel("bargauge", "Last 24 h per machine (in + out)",
      [(f'sum by (node) (increase(shrooms_host_receive_bytes_total{{{PHYS}}}[24h]) + increase(shrooms_host_transmit_bytes_total{{{PHYS}}}[24h]))', "{{node}}")],
      8, 9, x=16, unit="bytes")
advance(9)
panel("stat", "All machines, last 24 h",
      [(f'sum(increase(shrooms_host_receive_bytes_total{{{PHYS}}}[24h]) + increase(shrooms_host_transmit_bytes_total{{{PHYS}}}[24h]))', "total")],
      6, 4, unit="bytes")
panel("stat", "…of which delivery (rendezvous plane)",
      [('sum(increase(shrooms_delivery_bytes_total[24h]))', "delivery")], 6, 4, x=6, unit="bytes")
panel("stat", "…of which shrooms tunnels",
      [('sum(increase(shrooms_peer_receive_bytes_total[24h]) + increase(shrooms_peer_transmit_bytes_total[24h]))', "tunnels")],
      6, 4, x=12, unit="bytes", desc="Counted at both ends of each tunnel, and riding the physical interfaces of each.")
panel("stat", "Delivery share of physical traffic",
      [(f'sum(increase(shrooms_delivery_bytes_total[24h])) / sum(increase(shrooms_host_receive_bytes_total{{{PHYS}}}[24h]) + increase(shrooms_host_transmit_bytes_total{{{PHYS}}}[24h]))', "share")],
      6, 4, x=18, unit="percentunit")
advance(4)

row("Delivery — the rendezvous plane (a Core node relays for the whole cluster)")
panel("timeseries", "Delivery traffic per machine",
      [('sum by (node) (rate(shrooms_delivery_bytes_total{direction="in"}[5m]))', "{{node}} in"),
       ('-sum by (node) (rate(shrooms_delivery_bytes_total{direction="out"}[5m]))', "{{node}} out")], 16, 9)
panel("bargauge", "Delivery, last 24 h per machine",
      [('sum by (node) (increase(shrooms_delivery_bytes_total[24h]))', "{{node}}")], 8, 9, x=16, unit="bytes")
advance(9)
panel("timeseries", "Delivery peers",
      [('shrooms_delivery_peers', "{{node}}")], 12, 6, unit="short")
panel("timeseries", "Delivery messages per second",
      [('sum by (node) (rate(shrooms_delivery_messages_total[5m]))', "{{node}}")], 12, 6, unit="short")
advance(6)

row("Tunnels — shrooms between peers")
panel("timeseries", "Tunnel traffic per peer (as each machine counts it)",
      [('sum by (node, mesh, peer) (rate(shrooms_peer_receive_bytes_total[5m]) + rate(shrooms_peer_transmit_bytes_total[5m]))', "{{node}} ↔ {{peer}} ({{mesh}})")],
      16, 9)
panel("bargauge", "Tunnels, last 24 h per mesh",
      [('sum by (mesh) (increase(shrooms_peer_receive_bytes_total[24h]) + increase(shrooms_peer_transmit_bytes_total[24h]))', "{{mesh}}")],
      8, 9, x=16, unit="bytes", desc="Each tunnel counted at both ends.")
advance(9)
panel("table", "Peers now",
      [('shrooms_peer_up', "up"), ('shrooms_peer_relayed', "relayed")], 24, 8, unit="short",
      extra={"transformations": [{"id": "seriesToColumns", "options": {"byField": "peer"}}],
             "targets": [{"refId": "A", "expr": 'shrooms_peer_up', "format": "table", "instant": True},
                         {"refId": "B", "expr": 'shrooms_peer_relayed', "format": "table", "instant": True}]})
for p in panels:
    if p.get("type") == "table":
        for t in p["targets"]:
            t["datasource"] = {"type": "prometheus", "uid": "prometheus"}
        p["transformations"] = [{"id": "merge", "options": {}},
                                {"id": "organize", "options": {"excludeByName": {"Time": True, "__name__": True, "job": True, "instance": True},
                                                                "renameByName": {"Value #A": "up", "Value #B": "relayed"}}}]
advance(8)

# The load comes from the agent and from the daemon alike, whichever a
# machine runs: max by node takes one of the two, not their sum.
row("Machines — how loaded they are")
panel("timeseries", "CPU busy per machine",
      [('1 - max by (node) (sum by (node, job) (rate(shrooms_host_cpu_seconds_total{mode="idle"}[5m]))) / max by (node) (sum by (node, job) (rate(shrooms_host_cpu_seconds_total[5m])))', "{{node}}")],
      12, 8, unit="percentunit", desc="Share of all its CPUs' time each machine spent on something, not idle.")
panel("timeseries", "Load per CPU (5 min)",
      [('max by (node) (shrooms_host_load{window="5m"}) / max by (node) (shrooms_host_cpus)', "{{node}}")],
      12, 8, unit="short", desc="Above 1, a machine has more runnable work than CPUs.")
advance(8)
panel("bargauge", "Memory used",
      [('1 - max by (node) (shrooms_host_memory_bytes{kind="available"}) / max by (node) (shrooms_host_memory_bytes{kind="total"})', "{{node}}")],
      8, 7, unit="percentunit")
panel("bargauge", "Root disk free",
      [('max by (node) (shrooms_host_disk_bytes{kind="free"})', "{{node}}")], 8, 7, x=8, unit="bytes")
panel("bargauge", "CPUs",
      [('max by (node) (shrooms_host_cpus)', "{{node}}")], 8, 7, x=16, unit="short")
advance(7)

row("Agents — what the models did")
panel("timeseries", "Output tokens per second, by provider",
      [('sum by (provider) (rate(shrooms_agent_tokens_total{kind="output"}[5m]))', "{{provider}}")],
      12, 8, unit="short", desc="All machines' sessions together. Spikes are turns.")
panel("timeseries", "Tokens per second, by machine (all kinds)",
      [('sum by (node) (rate(shrooms_agent_tokens_total[5m]))', "{{node}}")],
      12, 8, unit="short", desc="Input, cache and output: what each machine's sessions sent and got.")
advance(8)
panel("bargauge", "Last 24 h, tokens by provider",
      [('sum by (provider) (increase(shrooms_agent_tokens_total[24h]))', "{{provider}}")], 8, 8, unit="short")
panel("bargauge", "Last 24 h, tokens by session (top 12)",
      [('topk(12, sum by (node, session) (increase(shrooms_agent_tokens_total[24h])))', "{{node}}/{{session}}")], 8, 8, x=8, unit="short")
panel("bargauge", "Last 24 h, cost by provider",
      [('sum by (provider) (increase(shrooms_agent_cost_usd_total[24h]))', "{{provider}}")], 8, 8, x=16, unit="currencyUSD",
      desc="As each harness prices its turns; 0 for a local model.")
advance(8)
panel("bargauge", "Cost so far, by provider",
      [('sum by (provider) (shrooms_agent_cost_usd_total)', "{{provider}}")], 8, 8, unit="currencyUSD",
      desc="Everything in the sessions' logs, from before metrics were scraped too. The 24 h panels count only what was added since.")
panel("bargauge", "Tokens so far, by provider",
      [('sum by (provider) (shrooms_agent_tokens_total)', "{{provider}}")], 8, 8, x=8, unit="short")
panel("bargauge", "Cost so far, by session (top 12)",
      [('topk(12, sum by (node, session) (shrooms_agent_cost_usd_total))', "{{node}}/{{session}}")], 8, 8, x=16, unit="currencyUSD")
advance(8)
panel("timeseries", "Model speed (output tokens/s of the last turn)",
      [('shrooms_agent_tokens_per_second', "{{node}}/{{session}} · {{model}}")],
      12, 8, unit="short", desc="Measured by each agent from the reply's stream: the writing speed, not the wait for the first token.")
panel("timeseries", "Sessions working",
      [('sum by (node) (shrooms_agent_sessions{state="working"})', "{{node}}")],
      12, 8, unit="short")
advance(8)

dash = {"uid": "shrooms-traffic", "title": "shrooms — where the bytes go", "tags": ["shrooms"], "timezone": "browser",
        "schemaVersion": 39, "version": 1, "refresh": "1m", "time": {"from": "now-24h", "to": "now"}, "panels": panels}
out = pathlib.Path(__file__).parent / "grafana" / "dashboards" / "shrooms-traffic.json"
out.write_text(json.dumps(dash, indent=1, ensure_ascii=False) + "\n")
print("wrote", out)
