import QtQuick

// The Shrooms Basecamp view on a made-up mesh, photographed for the site.
// Demo data only: no real machine, address or key appears in the pictures.
// Run by scripts/screenshots/shots.sh.
Item {
    id: top
    // "size=WxH" before the output directory renders at that size instead —
    // for the framed shot, which puts this view inside a real Basecamp window.
    readonly property var sizeArg: {
        var a = Qt.application.arguments
        for (var i = 0; i < a.length; i++) if (String(a[i]).indexOf("size=") === 0) return String(a[i]).slice(5).split("x")
        return null
    }
    readonly property string prefix: sizeArg ? "framed-" : ""
    width: sizeArg ? Number(sizeArg[0]) : 1500
    height: sizeArg ? Number(sizeArg[1]) : 940

    property string outDir: Qt.application.arguments[Qt.application.arguments.length - 1]
    readonly property var qrRows: ["11111110011001110111001111111","10000010110101110011101000001","10111010011100000010101011101","10111010011100111000001011101","10111010110011110111001011101","10000010011001110011001000001","11111110101010101010101111111","00000000011011010101100000000","10101010000110111101100010010","00000101010110001010111001001","01101111110001001010110100111","10011101011000001100101010010","11011011010000011100011001011","01010000110011000010111001001","01000111001000101010010001011","11111001000011111111000101010","00110010010000100100111101011","01001101000110101010101001101","10101010110001001110101010011","01001100010110000101001111010","10001011101100000110111110000","00000000111011001000100010111","11111110011100100101101011011","10000010010001110110100011011","10111010100110101100111110011","10111010001101101111110110111","10111010100101101111100111001","10000010011110011110101010010","11111110100100100101100011011"]

    function hist(base, wobble, n) {
        var out = []
        for (var i = 0; i < n; i++) out.push(Math.max(0, Math.round(base * (1 + wobble * Math.sin(i / 3) + 0.4 * Math.sin(i * 1.7)))))
        return out
    }
    function peer(name, mesh, o) {
        var p = { name: name, mesh: mesh, dns_name: name + "." + mesh + ".mesh", live: true, online: true,
                  relayed: false, rtt_ms: 0, rx_bps: 0, tx_bps: 0, tunnel_after_s: 0.4, handshake_age_s: 40,
                  expires: Math.floor(Date.now() / 1000) + 21 * 86400, device: "00" }
        for (var k in o) p[k] = o[k]
        return p
    }
    readonly property var status: ({
        name: "laptop", version: "v0.14.0", mode: "Edge", mode_running: "Edge", port_mapping: true,
        overlay: "fd5e:21c8:7d0a:4b1f:9e2c:113a:6f40:2b7d", prefix: "fd5e:21c8:7d0a::/48",
        rendezvous: { ok: true, status: "Connected" },
        dns: { registered: true, serving: true, suffix: "mesh", address: "fd5e:21c8:7d0a:4b1f:9e2c:113a:6f40:2b7d" },
        meshes: [
            { label: "home", overlay: "fd5e:21c8:7d0a:4b1f:9e2c:113a:6f40:2b7d", prefix: "fd5e:21c8:7d0a::/48", peers: 4, relays: 1,
              announce_services: true, announce_bound: true, relay: false, bound_here: ["ssh:22"], expires: Math.floor(Date.now() / 1000) + 24 * 86400,
              relay_using: "vps" },
            { label: "work", overlay: "fd91:0c3e:55aa:7e02:41d9:8b16:c2f0:9a31", prefix: "fd91:0c3e:55aa::/48", peers: 2, relays: 1,
              announce_services: false, relay: false, expires: Math.floor(Date.now() / 1000) + 18 * 86400 } ],
        peers: [
            peer("nas", "home", { rtt_ms: 3, rx_bps: 182000, tx_bps: 12400, overlay: "fd5e:21c8:7d0a:9a07:3c11:e4b2:7d55:1e08",
                 overlay_v4: "198.18.12.40", services: ["immich", "jellyfin"], bound: ["ssh:22"],
                 rx_history: hist(150000, 0.6, 40), tx_history: hist(12000, 0.5, 40) }),
            peer("phone", "home", { relayed: true, rtt_ms: 61, rx_bps: 2200, tx_bps: 1800, overlay: "fd5e:21c8:7d0a:61f2:08ad:5b94:c2e7:4410",
                 overlay_v4: "198.18.77.9", rx_history: hist(2000, 0.8, 40), tx_history: hist(1500, 0.6, 40) }),
            peer("vps", "home", { relay: true, rtt_ms: 24, rx_bps: 5200, tx_bps: 4900, overlay: "fd5e:21c8:7d0a:2e90:71c4:0d3b:9f62:a815",
                 overlay_v4: "198.18.3.201", services: ["grafana"], rx_history: hist(5000, 0.3, 40), tx_history: hist(4800, 0.3, 40) }),
            peer("pi", "home", { live: false, online: false, overlay: "fd5e:21c8:7d0a:c7d4:1fa9:6e20:38b5:07ce" }),
            peer("build-box", "work", { rtt_ms: 11, rx_bps: 41000, tx_bps: 96000, overlay: "fd91:0c3e:55aa:30d1:9e77:a204:5c18:e6b3",
                 overlay_v4: "198.18.40.17", bound: ["ci:8080"], rx_history: hist(40000, 0.7, 40), tx_history: hist(90000, 0.5, 40) }),
            peer("desk", "work", { relay: true, rtt_ms: 7, rx_bps: 900, tx_bps: 1100, overlay: "fd91:0c3e:55aa:8b62:04f1:d3ae:27c9:5d06",
                 rx_history: hist(900, 0.4, 40), tx_history: hist(1000, 0.4, 40) }) ]
    })

    property int holdPolls: 0
    QtObject {
        id: demoBridge
        property var callModule: function (module, method, args) {
            switch (method) {
            case "status": return JSON.stringify(top.status)
            case "getPref": return ""
            case "servicesOf": return JSON.stringify({ services: args[0] === "home" ? ["immich:2283", "notes:8080"] : [] })
            case "hostsSuffix": return JSON.stringify({ hosts_suffix: "mesh", default: "mesh" })
            case "blindRelays": return JSON.stringify({ relays: [], token_set: false })
            case "logs": return JSON.stringify({ lines: [] })
            case "inviteNew": return JSON.stringify({ token: "DEMO", grouped: "BEGUZ-N4WOX-PYMTR-CYKWT-QBYSX-U",
                                                      uri: "shrooms://enrol?token=DEMO", qr: top.qrRows, ttl_s: 900 })
            case "inviteHoldStart": return JSON.stringify({ started: true })
            case "inviteHoldProgress":
                return top.holdPolls++ < 1 ? JSON.stringify({ running: true })
                    : JSON.stringify({ done: true, result: { device_pub: "5f1e0c7a9b2d4e6f8a0c1e3b5d7f9a1c", wg_pub: "", seal_pub: "",
                                                             name: "kitchen-pi", eph_pub: "" } })
            }
            return JSON.stringify({ result: "ok" })
        }
        property var request: function (intent, params, cb) { cb({ ok: true, data: {}, error: "" }) }
    }

    Main { id: view; anchors.fill: parent; bridge: demoBridge }

    property int step: 0
    function shot(name, next) {
        top.grabToImage(function (r) {
            r.saveToFile(top.outDir + "/" + top.prefix + name)
            console.error("SHOT " + name)
            next()
        })
    }
    Timer {
        interval: 2600; running: true
        onTriggered: {
            view.servicesOpen = true
            settle.next = function () {
                top.shot("basecamp-mesh.png", function () {
                    view.servicesOpen = false
                    view.settingsOpen = true
                    settle.next = function () {
                        top.shot("basecamp-settings.png", function () {
                            view.settingsOpen = false
                            view.startInvite("home")
                            settle.next = function () {
                                top.shot("basecamp-invite.png", function () {
                                    view.pollInvite(); view.pollInvite()
                                    settle.next = function () {
                                        top.shot("basecamp-admit.png", function () { Qt.quit() })
                                    }
                                    settle.restart()
                                })
                            }
                            settle.restart()
                        })
                    }
                    settle.restart()
                })
            }
            settle.restart()
        }
    }
    Timer { id: settle; interval: 900; property var next: null; onTriggered: if (next) next() }
}
