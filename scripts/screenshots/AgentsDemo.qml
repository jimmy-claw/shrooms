import QtQuick

// The Shrooms Agents Basecamp view on made-up machines and sessions,
// photographed for the site. No real conversation, path or address appears.
// Run by scripts/screenshots/shots.sh.
Item {
    id: top
    width: 1500; height: 940

    property string outDir: Qt.application.arguments[Qt.application.arguments.length - 1]

    function iso(minAgo) { return new Date(Date.now() - minAgo * 60000).toISOString() }
    function task(id, session, from, state, title) {
        return { id: id, status: { state: state, timestamp: iso(4) },
                 metadata: { "shrooms/session": session, "shrooms/from": from, "shrooms/title": title } }
    }
    readonly property var limits: ({ at: iso(1), status: "allowed", window: "five_hour",
        windows: { five_hour: { utilization: 0.41, resets_at: new Date(Date.now() + 3 * 3600000).toISOString() },
                   seven_day: { utilization: 0.33, resets_at: new Date(Date.now() + 4 * 86400000).toISOString() } } })
    readonly property var hosts: [
        { name: "laptop", mesh: "home", address: "fd5e:21c8:7d0a:4b1f:9e2c:113a:6f40:2b7d",
          tasks: { tasks: [] },
          list: { limits: top.limits, sessions: [
            { name: "webapp", dir: "/home/ana/src/webapp", state: "waiting", pending: 1, running: true, starred: true,
              last_seq: 9, last_time: iso(1), context_used: 182000, context_window: 1000000, model: "claude-opus-5-5[1m]",
              preview: "The refactor is done and the tests pass. Pushing it now." },
            { name: "notes", dir: "/home/ana/notes", state: "idle", pending: 0, running: true,
              last_seq: 4, last_time: iso(38), context_used: 22000, context_window: 200000, model: "claude-sonnet-5-5",
              preview: "Added the meeting summary under 2026-10/." } ] } },
        { name: "homeserver", mesh: "home", address: "fd5e:21c8:7d0a:9a07:3c11:e4b2:7d55:1e08",
          tasks: { tasks: [ task("reviewer:t1", "reviewer", "laptop.home (laptop/webapp)", "TASK_STATE_WORKING", "Review the checkout refactor") ] },
          list: { limits: top.limits, sessions: [
            { name: "reviewer", dir: "/srv/review", state: "working", pending: 0, running: true,
              cage: { image: "localhost/shrooms-workbench:latest", sealed: true }, auto_approve: true,
              last_seq: 12, last_time: iso(0), context_used: 64000, context_window: 1000000, model: "claude-opus-5-5[1m]",
              preview: "Reading the diff the webapp session sent: 14 files, mostly cart/." },
            { name: "media", dir: "/srv/media", state: "asleep", pending: 0, running: false,
              last_seq: 2, last_time: iso(300), context_used: 9000, context_window: 200000, model: "qwen3-coder (pi)",
              preview: "Re-encoded the October folder." } ] } },
        { name: "build-box", mesh: "work", address: "fd91:0c3e:55aa:30d1:9e77:a204:5c18:e6b3",
          tasks: { tasks: [ task("ci:t2", "ci", "homeserver.home (homeserver/reviewer)", "TASK_STATE_WORKING", "Run the full suite on the branch") ] },
          list: { limits: top.limits, sessions: [
            { name: "ci", dir: "/home/ci/webapp", state: "working", pending: 0, running: true, auto_approve: true,
              cage: { image: "localhost/shrooms-workbench:latest" },
              last_seq: 6, last_time: iso(2), context_used: 31000, context_window: 1000000, model: "claude-sonnet-5-5",
              preview: "412 passed, 3 skipped. Running the browser tests." } ] } } ]

    function ev(seq, kind, data, by) { return { seq: seq, time: iso(12 - seq), kind: kind, by: by || "", data: data } }
    readonly property var events: [
        ev(1, "message", { text: "Refactor the checkout so the cart total is computed in one place, then run the tests." }, "phone.home"),
        ev(2, "claude", { type: "system", subtype: "init", session_id: "s1", model: "claude-opus-5-5[1m]" }),
        ev(3, "claude", { type: "assistant", message: { content: [
            { type: "text", text: "There are three places computing the total today:\n\n- `cart/summary.ts`\n- `checkout/review.tsx`\n- `api/orders.ts`\n\nI'll move it into `cart/total.ts` and make the others call it." },
            { type: "tool_use", id: "t1", name: "Edit", input: { file_path: "cart/total.ts", description: "Add totalOf()" } } ] } }),
        ev(4, "claude", { type: "user", message: { content: [ { type: "tool_result", tool_use_id: "t1", content: "ok", is_error: false } ] } }),
        ev(5, "claude", { type: "assistant", message: { content: [
            { type: "tool_use", id: "t2", name: "Bash", input: { command: "npm test", description: "Run the tests" } } ] } }),
        ev(6, "claude", { type: "user", message: { content: [ { type: "tool_result", tool_use_id: "t2", content: "Tests: 128 passed, 128 total", is_error: false } ] } }),
        ev(7, "claude", { type: "assistant", message: { content: [
            { type: "text", text: "All **128 tests pass**. I've asked the reviewer on the home server to look the diff over before it goes out." } ] } }),
        ev(8, "claude", { type: "assistant", message: { content: [
            { type: "tool_use", id: "t3", name: "Bash", input: { command: "git push origin checkout-total" } } ] } }),
        ev(9, "claude", { type: "control_request", request_id: "p1", request: { subtype: "can_use_tool", tool_name: "Bash",
            input: { command: "git push origin checkout-total" }, description: "Push the branch" } }),
    ]

    QtObject {
        id: demoBridge
        function callModule(module, method, args) {
            switch (method) {
            case "status": return JSON.stringify({ name: "laptop", meshes: [ { label: "home" }, { label: "work" } ],
                peers: top.hosts.map(function (h) { return { name: h.name, mesh: h.mesh, overlay: h.address, online: true } }) })
            case "getPref": return ""
            case "agentsFind": return JSON.stringify(top.hosts)
            case "agentWatch": return JSON.stringify({ ok: true })
            case "agentEvents":
                var after = Number(args[0])
                return JSON.stringify({ next: top.events.length, more: false, connected: true, error: "",
                                        events: after >= top.events.length ? [] : top.events.slice(after) })
            case "agentGet":
                if (String(args[1]) === "/v1/harnesses") return JSON.stringify({ harnesses: [
                    { name: "claude", title: "Claude Code", caps: { approve: true, takeover: true } } ] })
                if (String(args[1]).indexOf("/v1/conversations") === 0) return JSON.stringify({ conversations: [] })
                return JSON.stringify({ history: [] })
            case "agentJobs": return JSON.stringify({ recording: false, jobs: [] })
            case "agentOutbox": return JSON.stringify([])
            case "agentVoice": return JSON.stringify({ installed: true, busy: false, engine: "piper" })
            case "agentSpeak": return JSON.stringify({ speaking: false })
            case "agentGather": return JSON.stringify({ gather: 1 })
            case "agentGathered": return JSON.stringify({ id: 1, results: [] })
            }
            return JSON.stringify({ ok: true })
        }
    }

    Main { id: view; anchors.fill: parent; bridge: demoBridge }

    function shot(name, next) {
        top.grabToImage(function (r) { r.saveToFile(top.outDir + "/" + name); console.error("SHOT " + name); next() })
    }
    Timer { id: settle; interval: 1200; property var next: null; onTriggered: if (next) next() }
    Timer {
        interval: 2500; running: true
        onTriggered: {
            var laptop = view.agentHosts.filter(function (h) { return h.name === "laptop" })[0]
            view.openSession(laptop, "webapp")
            settle.next = function () {
                top.shot("agents-session.png", function () {
                    view.boardMode = true
                    view.showBoard()
                    settle.next = function () { top.shot("agents-board.png", function () { Qt.quit() }) }
                    settle.restart()
                })
            }
            settle.restart()
        }
    }
}
