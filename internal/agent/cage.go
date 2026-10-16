package agent

// Cages (ADR-037, docs/agents-in-cages.md): a session whose process runs in a
// rootless podman container of its own instead of as the owner's user on the
// machine. Root inside, so the agent can install what its work needs; the
// owner's user outside, so the project's files stay the owner's and the rest
// of the machine is out of reach.
//
// The container is made with the session's first start and kept: it runs
// `sleep infinity`, the harness is run in it with `podman exec`, and it is
// stopped when the harness's process ends — which also ends anything the
// session left running — and started again with the next. What the agent
// installed stays until the session is deleted, and the container with it.
//
// Inside, the paths are the machine's: the project at its own path, the
// harness's settings and transcripts (~/.claude, ~/.pi/agent) at theirs, the
// files sent to sessions, shrooms-agent and the daemon's socket (the shrooms
// MCP server). So a caged conversation resumes, is read and searched exactly
// as one that is not. Nothing else of the owner's home is there.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed workbench.Containerfile
var workbenchContainerfile []byte

// WorkbenchImage is the image cages are made from unless the machine or the
// session names another: built here from workbench.Containerfile. And
// DesktopImage is the workbench with an X server, to run, drive and record
// apps in.
const (
	WorkbenchImage = "localhost/shrooms-workbench:latest"
	DesktopImage   = "localhost/shrooms-workbench:desktop"
)

// builtin are the images built here, from workbench.Containerfile's stages.
var builtin = map[string]string{WorkbenchImage: "workbench", DesktopImage: "desktop"}

// Cage is how a session is caged; nil for a session that is not.
type Cage struct {
	// Image is the session's own; "" for the machine's (Cages.Image).
	Image string `json:"image,omitempty"`
	// Container is podman's name for it, given when the session is made:
	// the session's name then, and a few random letters, so a rename leaves
	// it be and a later session of the same name gets its own.
	Container string `json:"container,omitempty"`
	// Nix gives the cage the machine's nix: its store and profile, and
	// builds through its daemon where it has one. A single-user nix has
	// none, and the cage then writes the store itself — as the owner would.
	Nix bool `json:"nix,omitempty"`
	// GitHub gives it the owner's GitHub CLI login (~/.config/gh), read-only.
	GitHub bool `json:"github,omitempty"`
	// Shared gives it the folder the caged sessions it was made alongside
	// share, read-write at /shared (ADR-057): the one of its meshes, so a
	// session limited to the home mesh shares nothing with an office one.
	Shared bool `json:"shared,omitempty"`
	// Memory and CPUs are this cage's limits, "" for the machine's (Cages);
	// a podman memory ("16g") and a number of CPUs ("8"). Changed on a
	// cage that exists without making it again (SetCageLimits), so what
	// was installed in it stays.
	Memory string `json:"memory,omitempty"`
	CPUs   string `json:"cpus,omitempty"`
	// Sealed: for code nobody vouches for (sealed.go, ADR-045) — the
	// internet and nothing local, a credential of its own, no agents to
	// ask, results through Outbox (a directory on the machine).
	Sealed bool   `json:"sealed,omitempty"`
	Outbox string `json:"outbox,omitempty"`
}

// newCage is the cage a session is given from what was asked: its own
// container name, and for a sealed one its outbox and none of the options
// that widen a cage.
func newCage(asked *Cage, session string) *Cage {
	c := &Cage{Image: asked.Image, Nix: asked.Nix, GitHub: asked.GitHub, Shared: asked.Shared, Sealed: asked.Sealed,
		Memory: asked.Memory, CPUs: asked.CPUs, Container: newContainerName(session)}
	if c.Sealed {
		c.Nix, c.GitHub, c.Shared = false, false, false
		c.Outbox = sealedOutbox(session)
	}
	return c
}

// cageNetwork gives a cage an address of its own and a default route through
// pasta, which carries what the cage sends out on the machine's own sockets.
// Without it pasta copies the addresses and routes of one of the machine's
// interfaces — a mesh's, on a machine with no IPv6 default route — and the
// other meshes, and this machine's own agent on that one, are not reachable.
const cageNetwork = "pasta:-a,fd5e:ca9e:1::2,-g,fd5e:ca9e:1::1"

// Cages is what this machine makes cages with.
type Cages struct {
	Podman string // the podman binary
	Image  string // the machine's image; WorkbenchImage when ""
	// Limits for each cage; "" or 0 for none.
	Memory string
	CPUs   string
	Pids   int
	// Socket is the shrooms daemon's, which the shrooms MCP server reads.
	Socket string
	// Nft is the nft program closing the agent port in cages; "" finds it.
	Nft string
	// SystemdRun starts cages in a systemd scope of their own; "" starts
	// them as the agent's children (startCage).
	SystemdRun string
	// volumesMu guards the cached volume directory below, apart from c.mu:
	// finding it shells out to podman, which must not hold up the cages.
	volumesMu  sync.Mutex
	volumesDir string
	volumesAt  time.Time
	// SealedToken is the file with the Claude Code token sealed cages run
	// on; "" for sealed-claude-token in the state directory.
	SealedToken string

	mu       sync.Mutex
	building bool
	buildErr string
}

// CageInfo is a session's cage as the list shows it.
type CageInfo struct {
	Image  string `json:"image"`
	Nix    bool   `json:"nix,omitempty"`
	GitHub bool   `json:"github,omitempty"`
	// Shared: the volume of the folder it shares (ADR-057), "" for none.
	Shared string `json:"shared,omitempty"`
	// Memory and CPUs: its limits as set for it, "" for the machine's; and
	// the machine's, for the apps to show what "" means.
	Memory        string `json:"memory,omitempty"`
	CPUs          string `json:"cpus,omitempty"`
	MachineMemory string `json:"machine_memory,omitempty"`
	MachineCPUs   string `json:"machine_cpus,omitempty"`
	Sealed        bool   `json:"sealed,omitempty"`
	Outbox        string `json:"outbox,omitempty"`
}

// CageStatus is what the apps are told: whether sessions can be caged here.
type CageStatus struct {
	Available bool   `json:"available"`
	Image     string `json:"image,omitempty"`
	// Images are those offered: the machine's, and the ones built here.
	Images []string `json:"images,omitempty"`
	// Nix: the machine has nix to give a cage.
	Nix bool `json:"nix,omitempty"`
	// Sealed: the machine has a token for sealed cages (sealed.go).
	Sealed bool `json:"sealed,omitempty"`
	// Shared: a cage can be given a shared folder (ADR-057), at SharedIn in
	// it. SharedRoot is podman's directory of volumes on this machine, so the
	// app can show where a session's folder is (SharedRoot/VOLUME/_data)
	// rather than making someone find a volume by hand; "" when unknown.
	Shared     bool   `json:"shared,omitempty"`
	SharedIn   string `json:"shared_in,omitempty"`
	SharedRoot string `json:"shared_root,omitempty"`
	Ready      bool   `json:"ready"`              // the image is there
	Building   bool   `json:"building,omitempty"` // it is being built
	Error      string `json:"error,omitempty"`    // why the last build failed
}

// NewCages finds podman; nil when the machine has none.
func NewCages(image string) *Cages {
	p, err := exec.LookPath("podman")
	if err != nil {
		return nil
	}
	if image == "" {
		image = WorkbenchImage
	}
	c := &Cages{Podman: p, Image: image, Memory: "4g", CPUs: "2", Pids: 2048, Socket: "/run/shrooms/shrooms.sock"}
	// Under systemd (INVOCATION_ID is set for a unit's processes): a scope of
	// their own for the cages, see startCage.
	if os.Getenv("INVOCATION_ID") != "" {
		if sr, err := exec.LookPath("systemd-run"); err == nil {
			c.SystemdRun = sr
		}
	}
	return c
}

// startCage starts a cage, and makes sure it has its network.
//
// `podman start` forks pasta, which carries the cage's network, in the
// caller's cgroup: the agent's service. Restarting the agent then killed
// pasta while the container ran on, and every caged session on the machine
// had loopback only and "Connection error." on each turn (proteus, the
// 2026-10-15 rollout). Under systemd the cage is started in a transient scope
// of its own, which a restart of the agent leaves alone. A cage still running
// without its network (started before this, or by an agent not under
// systemd) is stopped and started again.
func (c *Cages) startCage(ctx context.Context, name string) error {
	start := func() error {
		if c.SystemdRun == "" {
			_, err := c.run(ctx, "start", name)
			return err
		}
		var errb bytes.Buffer
		cmd := exec.CommandContext(ctx, c.SystemdRun, "--user", "--scope", "--quiet", "--collect",
			"--description=shrooms cage "+name, "--", c.Podman, "start", name)
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("podman start: %s", lastLine(strings.TrimSpace(errb.String()+" "+err.Error())))
		}
		return nil
	}
	if err := start(); err != nil {
		return err
	}
	if c.hasNetwork(ctx, name) {
		return nil
	}
	c.run(ctx, "stop", "-t", "3", name)
	if err := start(); err != nil {
		return err
	}
	if !c.hasNetwork(ctx, name) {
		return fmt.Errorf("the cage %s has no network after starting it again", name)
	}
	return nil
}

// hasNetwork is whether a running cage has an interface besides loopback:
// none means its pasta is gone.
func (c *Cages) hasNetwork(ctx context.Context, name string) bool {
	out, err := c.run(ctx, "exec", name, "cat", "/proc/net/if_inet6")
	if err != nil {
		return true // cannot tell; the harness's own errors will say
	}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 6 && f[5] != "lo" {
			return true
		}
	}
	return false
}

func (c *Cages) run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Podman, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("podman %s: %s", args[0], lastLine(msg))
	}
	return out.String(), nil
}

func (c *Cages) image(cg *Cage) string {
	if cg != nil && cg.Image != "" {
		return cg.Image
	}
	return c.Image
}

func (c *Cages) imageExists(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := c.run(ctx, "image", "exists", image)
	return err == nil
}

// workbenchLabel carries a hash of the Containerfile an image of ours was
// built from: an agent with a newer one builds it again.
const workbenchLabel = "xyz.vpavlin.shrooms.containerfile"

func workbenchHash() string {
	sum := sha256.Sum256(workbenchContainerfile)
	return hex.EncodeToString(sum[:8])
}

// current: an image of ours built from this agent's Containerfile.
func (c *Cages) current(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := c.run(ctx, "image", "inspect", "--format", "{{index .Labels \""+workbenchLabel+"\"}}", image)
	return err == nil && strings.TrimSpace(out) == workbenchHash()
}

// Status says whether the machine's image is ready.
func (c *Cages) Status() CageStatus {
	if c == nil {
		return CageStatus{}
	}
	// Resolved BEFORE the lock: it shells out to podman (bounded, but up to a
	// few seconds), and holding c.mu across that would block every other cage
	// operation on the machine.
	volumes := c.volumesPath()
	c.mu.Lock()
	st := CageStatus{Available: true, Image: c.Image, Building: c.building, Error: c.buildErr,
		Images: []string{c.Image}, Nix: nixHere(), Sealed: tokenIn(c.SealedToken)}
	c.mu.Unlock()
	for _, im := range []string{WorkbenchImage, DesktopImage} {
		if im != c.Image {
			st.Images = append(st.Images, im)
		}
	}
	st.Ready = !st.Building && c.imageExists(c.Image)
	st.Shared, st.SharedIn, st.SharedRoot = true, CageSharedDir, volumes
	return st
}

// volumesPath is podman's directory of volumes, where the owner finds a
// shared folder: VOLUME/_data under it. Only volumes ensureSharedVolume let
// through are mounted, and those are local ones with no options, which is
// where podman keeps them.
func (c *Cages) volumesPath() string {
	if c == nil || c.Podman == "" {
		return ""
	}
	// A podman machine on macOS reports a path inside its VM, which does not
	// exist where the app runs: better no path than one that opens nothing.
	if runtime.GOOS != "linux" {
		return ""
	}
	// Cached: Status() is polled by the apps, and a slow or hung podman would
	// otherwise stall the whole status view for the timeout, every poll.
	c.volumesMu.Lock()
	defer c.volumesMu.Unlock()
	if time.Since(c.volumesAt) < sharedPathTTL {
		return c.volumesDir
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedPathTimeout)
	defer cancel()
	p := ""
	if out, err := c.run(ctx, "info", "--format", "{{.Store.VolumePath}}"); err == nil {
		p = strings.TrimSpace(out)
	}
	c.volumesDir, c.volumesAt = p, time.Now()
	return p
}

// ensureSharedVolume makes the shared folder's volume, or checks that the one
// there is what this would have made: local, with no options. The names are
// fixed, so whoever makes one first decides what it is, and one made as a bind
// (`--opt o=bind --opt device=DIR`) would hand DIR read-write to every cage
// given it. Checked before every cage is made, not only shown.
func (c *Cages) ensureSharedVolume(ctx context.Context, name string) error {
	out, err := c.run(ctx, "volume", "inspect", "--format", "{{.Driver}} {{.Options}}", name)
	if err != nil {
		if _, err := c.run(ctx, "volume", "create", "--label", sharedLabel+"=1", name); err != nil {
			return fmt.Errorf("the shared folder's volume %s: %w", name, err)
		}
		return nil
	}
	if got := strings.TrimSpace(out); got != "local map[]" {
		return fmt.Errorf("the volume %s is not a plain one (%s), so it is not given to cages as their shared folder: remove it (podman volume rm %s), or move the session into a cage without the shared folder", name, got, name)
	}
	return nil
}

const (
	// sharedPathTTL is how long the volume directory is reused: a poll does
	// not shell out each time.
	sharedPathTTL = 60 * time.Second
	// sharedPathTimeout bounds the one lookup that is actually made. Shorter
	// than the poll interval on purpose: a status view should be stale, not
	// blocked.
	sharedPathTimeout = 3 * time.Second
)

// Prepare builds an image of ours, in the background, if it is the one a
// cage needs and it is not there, or was built from an older Containerfile
// (the old one serves until then). Another image is the owner's to provide.
func (c *Cages) Prepare(image string, log func(msg string, args ...any)) {
	target, ours := builtin[image]
	if !ours || c.current(image) {
		return
	}
	c.mu.Lock()
	if c.building {
		c.mu.Unlock()
		return
	}
	c.building, c.buildErr = true, ""
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		log("building the workbench image for cages", "image", image)
		// On the machine's network: a build's own, through pasta, timed
		// out fetching from npm on the laptop (2026-10-08).
		cmd := exec.CommandContext(ctx, c.Podman, "build", "--network", "host", "--target", target,
			"--label", workbenchLabel+"="+workbenchHash(), "-t", image, "-f", "-", os.TempDir())
		cmd.Stdin = bytes.NewReader(workbenchContainerfile)
		out, err := cmd.CombinedOutput()
		c.mu.Lock()
		c.building = false
		if err != nil {
			c.buildErr = "building the workbench image: " + lastLine(strings.TrimSpace(string(out)))
		}
		c.mu.Unlock()
		log("workbench image", "image", image, "err", err)
	}()
}

// newContainerName is a new session's container's name.
func newContainerName(session string) string {
	b := make([]byte, 3)
	rand.Read(b)
	return "shrooms-" + session + "-" + hex.EncodeToString(b)
}

// mount is one path of the machine's in a cage, at the same path.
//
// volume says `path` is a podman named volume, not a path on this machine. A
// volume has no path to stat, so the mount filter must not drop it for not
// existing.
type mount struct {
	path   string
	ro     bool
	at     string // where in the cage, when not at the same path
	volume bool
}

// CageSharedDir is where a cage given the shared folder sees it, and the one
// path in a cage that is not this machine's. It is a podman named volume, so
// the machine's own filesystem is not what a cage writes into: the owner (and
// the agent, which reads it as the user that owns it) can look at it.
const CageSharedDir = "/shared"

// sharedVolume is the shared folder's volume for sessions on every mesh.
// Deliberately not a host path: a folder the owner keeps on the machine and a
// folder cages share are different things, and only one of them may be written
// by a cage.
const sharedVolume = "shrooms-shared"

// sharedVolumeFor is the shared folder's volume for a session on these meshes
// (none is all): one per set of meshes, so sessions share only with sessions
// seen from the same meshes (ADR-049). The labels in the name are for a person
// reading `podman volume ls`; the hash keeps two sets apart that the name's
// alphabet would make look alike.
func sharedVolumeFor(meshes []string) string {
	if len(meshes) == 0 {
		return sharedVolume
	}
	ms := append([]string(nil), meshes...)
	sort.Strings(ms)
	sum := sha256.Sum256([]byte(strings.Join(ms, "\n")))
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, strings.Join(ms, "_"))
	if len(name) > 40 {
		name = name[:40]
	}
	return sharedVolume + "-" + name + "-" + hex.EncodeToString(sum[:3])
}

// sharedFor is the volume a session's cage is given as its shared folder, or
// "" for none. Called with s.mu held.
func (s *Session) sharedFor() string {
	if s.cage == nil || !s.cage.Shared || s.cage.Sealed {
		return ""
	}
	return sharedVolumeFor(s.meshes)
}

// cageRun is what one start of a caged session's process needs.
type cageRun struct {
	c         *Cages
	container string
	dir       string
	envFile   string
	bin       string // the harness's program inside
}

// cageMounts are the machine's paths a session's cage sees.
func (m *Manager) cageMounts(s *Session, harness string) []mount {
	home, _ := os.UserHomeDir()
	// The files sent to sessions, read-only: all of them, since a session
	// renamed keeps finding what was sent before under its old name.
	up := filepath.Join(m.dir, "uploads")
	os.MkdirAll(up, 0o700)
	ms := []mount{{path: s.dir}, {path: up, ro: true}}
	// The shared folder, read-write, when it was asked for: a caged session is
	// the owner's own agent, and the point of the folder is that several of
	// them work in it together.
	if v := s.sharedFor(); v != "" {
		ms = append(ms, mount{path: v, at: CageSharedDir, volume: true})
	}
	switch harness {
	case "claude":
		dir := filepath.Join(home, ".claude")
		ms = append(ms, mount{path: dir})
		ms = append(ms, readOnlyIn(dir, claudeHostCode)...)
		if bin, err := claudeProgram(m.binOf("claude")); err == nil {
			ms = append(ms, mount{path: filepath.Dir(bin), ro: true})
		}
	case "pi":
		dir := piAgentDir()
		ms = append(ms, mount{path: dir})
		ms = append(ms, readOnlyIn(dir, piHostCode)...)
	}
	if s.cage.Nix {
		ms = append(ms, nixMounts(home)...)
	}
	// Its own agent's socket, as a directory: one made again after the agent
	// restarts is the one the cage sees.
	pd := m.proxyDir(s.cage.Container)
	os.MkdirAll(pd, 0o700)
	ms = append(ms, mount{path: pd, at: CageProxyDir, ro: true})
	if s.cage.GitHub {
		ms = append(ms, mount{path: filepath.Join(home, ".config", "gh"), ro: true})
	}
	if m.Self != "" {
		if self, err := filepath.EvalSymlinks(m.Self); err == nil {
			ms = append(ms, mount{path: self, ro: true})
			if self != m.Self {
				ms = append(ms, mount{path: m.Self, ro: true})
			}
		}
	}
	// Not the shrooms daemon's control socket. It was mounted for the
	// shrooms tools to find the machines, but the agent's user's tier on it
	// can leave or join a mesh, change the relay, the services and what is
	// announced, and restart the daemon — the machine, from a cage (found
	// 2026-10-09). The tools find the machines through the cage's own socket
	// to its agent now (cageproxy.go).
	return keepMounts(ms)
}

// keepMounts drops the mounts that are not there. A named volume is not a path
// here (ensureSharedVolume makes it), so stat'ing one would drop the shared
// folder silently - which is why the volume case is in this function and not
// repeated in each caller.
func keepMounts(ms []mount) []mount {
	var out []mount
	for _, x := range ms {
		if x.volume {
			out = append(out, x)
			continue
		}
		if _, err := os.Stat(x.path); err == nil {
			out = append(out, x)
		}
	}
	return out
}

// The harness's settings directory is the cage's to write — its login is
// refreshed there, its transcripts kept — but not what the harness runs or
// obeys on the machine: a caged agent that could write a hook into
// ~/.claude/settings.json, or an extension into ~/.pi/agent, would have it run
// on the host by the next session outside a cage. Those paths are mounted
// again, read-only, over the writable directory.
var (
	claudeHostCode = []string{"settings.json", "settings.local.json", "CLAUDE.md", "hooks", "skills", "plugins",
		"commands", "agents", "output-styles", "bin"}
	piHostCode = []string{"settings.json", "models.json", "mcp.json", "extensions", "skills", "prompts", "themes",
		"AGENTS.md", "SYSTEM.md", "APPEND_SYSTEM.md", "bin"}
)

// readOnlyIn is each of names under dir, read-only (cageMounts keeps only
// those that exist).
func readOnlyIn(dir string, names []string) []mount {
	out := make([]mount, len(names))
	for i, n := range names {
		out[i] = mount{path: filepath.Join(dir, n), ro: true}
	}
	return out
}

// claudeProgram is Claude Code's program itself, symlinks followed: the
// native one, or npm's cli.js, which the workbench's node runs.
func claudeProgram(bin string) (string, error) {
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// nixDaemonSocket is where a multi-user nix's daemon listens.
const nixDaemonSocket = "/nix/var/nix/daemon-socket"

func nixHere() bool {
	_, err := os.Stat("/nix/store")
	return err == nil
}

// nixMounts give a cage the machine's nix. With a daemon, the store is read
// and builds go to the daemon, as for any user; a single-user nix is the
// owner's own, and the cage writes it as the owner.
func nixMounts(home string) []mount {
	if !nixHere() {
		return nil
	}
	ms := []mount{{path: filepath.Join(home, ".local", "state", "nix")}, {path: filepath.Join(home, ".config", "nix"), ro: true}}
	if _, err := os.Stat(nixDaemonSocket); err == nil {
		return append(ms, mount{path: "/nix", ro: true}, mount{path: nixDaemonSocket})
	}
	return append(ms, mount{path: "/nix"})
}

// cagePath is the PATH of a cage's processes: the image's, after nix's
// profile when the cage has nix.
func cagePath(home string, nix bool) string {
	p := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if nix {
		p = filepath.Join(home, ".local", "state", "nix", "profiles", "profile", "bin") + ":/nix/var/nix/profiles/default/bin:" + p
	}
	return p
}

func piAgentDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

func (m *Manager) binOf(harness string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bins[harness]
}

// createArgs makes a session's container.
func (c *Cages) createArgs(name, session, image string, ms []mount, extra ...string) []string {
	return c.createArgsWith(nil, name, session, image, ms, extra...)
}

// limits are a cage's memory and CPUs: its own, or the machine's.
func (c *Cages) limits(cage *Cage) (memory, cpus string) {
	memory, cpus = c.Memory, c.CPUs
	if cage != nil && cage.Memory != "" {
		memory = cage.Memory
	}
	if cage != nil && cage.CPUs != "" {
		cpus = cage.CPUs
	}
	return memory, cpus
}

// createArgsWith makes a session's container, with its cage's own limits.
func (c *Cages) createArgsWith(cage *Cage, name, session, image string, ms []mount, extra ...string) []string {
	args := []string{"create", "--name", name, "--label", "xyz.vpavlin.shrooms.session=" + session,
		// A cage made with its own agent's socket (ADR-044); one without is
		// made again.
		"--label", proxyLabel + "=" + cageGeneration,
		// label=disable, NOT :Z. A :Z relabel on a mount shared between
		// containers takes it away from the others on an SELinux host
		// (measured 2026-10-14); do not "harden" this to :Z.
		"--network", cageNetwork, "--security-opt", "label=disable",
		// Not in podman's defaults either; said so, since the rule that
		// closes the agent port depends on it.
		"--cap-drop", "NET_ADMIN"}
	memory, cpus := c.limits(cage)
	if memory != "" {
		args = append(args, "--memory", memory)
	}
	if cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if c.Pids > 0 {
		args = append(args, "--pids-limit", fmt.Sprint(c.Pids))
	}
	for _, x := range ms {
		at := x.path
		if x.at != "" {
			at = x.at
		}
		var opts []string
		if x.ro {
			opts = append(opts, "ro")
		}
		// nocopy: an empty named volume is seeded from the image's content at
		// the mount point on first mount - including from an owner-provided
		// image, and even on a :ro mount, since the copy comes from the image
		// side. Costs nothing and keeps the shared folder empty.
		if x.volume {
			opts = append(opts, "nocopy")
		}
		// After a colon, comma-separated: "VOL:/shared,nocopy" is a mount
		// at a directory called "/shared,nocopy" (podman 5, 2026-10-14).
		v := x.path + ":" + at
		if len(opts) > 0 {
			v += ":" + strings.Join(opts, ",")
		}
		args = append(args, "-v", v)
	}
	args = append(args, extra...)
	return append(args, image, "sleep", "infinity")
}

// prepare makes the session's container if it has none, starts it, and says
// how to run the harness in it. Called with s.mu held.
func (m *Manager) prepareCage(s *Session, bin string) (*cageRun, error) {
	c := m.Cages
	if c == nil {
		return nil, errors.New("this session is caged, and this machine has no podman to cage it with")
	}
	image := c.image(s.cage)
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	name := s.cage.Container
	if _, err := c.run(ctx, "container", "exists", name); err == nil {
		// Made before cages had their own agent's socket: made again, and
		// what was installed in it goes (ADR-044).
		out, _ := c.run(ctx, "inspect", "--format", "{{index .Config.Labels \""+proxyLabel+"\"}}", name)
		sealedOut := sealedGeneration
		if s.cage.Sealed {
			sealedOut, _ = c.run(ctx, "inspect", "--format", "{{index .Config.Labels \""+sealedLabel+"\"}}", name)
		}
		// Its shared folder, which is set when it is made: one asked for
		// later, or another one since its meshes changed, is a new cage.
		sharedOut, _ := c.run(ctx, "inspect", "--format", "{{index .Config.Labels \""+sharedLabel+"\"}}", name)
		if v := strings.TrimSpace(sharedOut); v == "<no value>" {
			sharedOut = ""
		}
		why := ""
		switch {
		case strings.TrimSpace(out) != cageGeneration || strings.TrimSpace(sealedOut) != sealedGeneration:
			why = "made again for a newer generation of the cage"
		case strings.TrimSpace(sharedOut) != s.sharedFor():
			why = "made again for its shared folder"
		}
		if why != "" {
			c.run(ctx, "rm", "-f", "-t", "3", name)
			s.record("caged", "shrooms", map[string]any{"caged": true, "image": image, "nix": s.cage.Nix, "github": s.cage.GitHub,
				"shared": s.cage.Shared, "remade": why + "; what was installed in it is gone"})
		}
	}
	if _, err := c.run(ctx, "container", "exists", name); err != nil {
		if !c.imageExists(image) {
			c.Prepare(image, m.log.Info)
			if st := c.Status(); st.Error != "" {
				return nil, errors.New(st.Error)
			}
			if _, ours := builtin[image]; ours {
				return nil, errors.New("the image for this session's cage is being built (a few minutes, the first time); send again then")
			}
			return nil, fmt.Errorf("there is no image %s on this machine for this session's cage", image)
		}
		ms := m.cageMounts(s, s.harness.Name())
		var extra []string
		if s.cage.Sealed {
			var err error
			if ms, err = m.sealedMounts(s); err != nil {
				return nil, err
			}
			prof, err := m.sealedSeccomp()
			if err != nil {
				return nil, err
			}
			extra = append([]string{"--security-opt", "seccomp=" + prof, "--label", sealedLabel + "=" + sealedGeneration}, sealedDNSArgs()...)
		}
		if v := s.sharedFor(); v != "" {
			if err := c.ensureSharedVolume(ctx, v); err != nil {
				return nil, err
			}
			extra = append(extra, "--label", sharedLabel+"="+v)
		}
		if _, err := c.run(ctx, c.createArgsWith(s.cage, name, s.Name(), image, ms, extra...)...); err != nil {
			return nil, err
		}
	}
	if err := m.startCageProxy(s, name); err != nil {
		return nil, fmt.Errorf("the cage's socket to its agent: %w", err)
	}
	if err := c.startCage(ctx, name); err != nil {
		return nil, err
	}
	if err := c.closeAgentPort(ctx, name, s.cage.Sealed); err != nil {
		// Not run with the port open: it would reach agents as this machine.
		c.run(ctx, "stop", "-t", "3", name)
		return nil, err
	}
	// The forwarder to the gateway, on the cage's loopback, before the
	// harness that will use it (ADR-059). It holds no secret; a second one
	// in a cage that kept running finds the port taken and goes.
	if m.gatewayed(s) {
		if _, err := c.run(ctx, "exec", "-d", name, m.Self, "llm-forward",
			"--listen", GatewayListen, "--socket", CageProxyDir+"/proxy.sock"); err != nil {
			return nil, fmt.Errorf("the cage's way to its model: %w", err)
		}
	}
	env, err := m.cageEnv(s)
	if err != nil {
		return nil, err
	}
	r := &cageRun{c: c, container: name, dir: s.dir, envFile: env, bin: s.harness.Bin()}
	if s.harness.Name() == "claude" {
		if p, err := claudeProgram(bin); err == nil {
			r.bin = p
		}
	}
	return r, nil
}

// cageEnv writes what a caged process is started with, to a file only the
// owner reads: keys stay out of the process list.
func (m *Manager) cageEnv(s *Session) (string, error) {
	if s.cage.Sealed {
		vars, err := m.sealedEnv(s)
		if err != nil {
			return "", err
		}
		return m.writeCageEnv(s, vars)
	}
	home, _ := os.UserHomeDir()
	vars := map[string]string{
		"HOME": home,
		// Claude Code keeps its settings file (.claude.json) beside its
		// home directory, where nothing of the owner's is mounted; this
		// keeps it in ~/.claude, which is.
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		// Root inside: Claude Code refuses to skip permissions as root
		// except in a sandbox, which a cage is.
		"IS_SANDBOX":            "1",
		"SHROOMS_AGENT_SESSION": s.Name(),
		// The shrooms tools reach agents through this (ADR-044).
		"SHROOMS_AGENT_PROXY": CageProxyDir + "/proxy.sock",
		"PATH":                cagePath(home, s.cage.Nix),
	}
	// Where the folder cages share is mounted (ADR-057), only in a cage that
	// has it. Told, not discovered: a folder nobody is told about is a folder
	// nobody uses.
	if s.cage.Shared {
		vars["SHROOMS_SHARED"] = CageSharedDir
	}
	if s.cage.Nix {
		if _, err := os.Stat(nixDaemonSocket); err == nil {
			vars["NIX_REMOTE"] = "daemon"
		}
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if passedToCage(k, s.cage.GitHub) {
			vars[k] = v
		}
	}
	return m.writeCageEnv(s, vars)
}

// writeCageEnv writes a cage's environment file, which only the owner reads.
func (m *Manager) writeCageEnv(s *Session, vars map[string]string) (string, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if strings.ContainsAny(vars[k], "\n") {
			continue
		}
		b.WriteString(k + "=" + vars[k] + "\n")
	}
	dir := filepath.Join(m.dir, "cages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, s.cage.Container+".env")
	return p, os.WriteFile(p, []byte(b.String()), 0o600)
}

// passedToCage: the agent's environment variables a caged harness is given —
// the keys and settings of models and providers, not the machine's own (its
// PATH, its desktop, its user).
func passedToCage(k string, github bool) bool {
	// GitHub's tokens only to a cage given the GitHub login: otherwise the
	// *_TOKEN rule below handed the owner's GitHub to every cage.
	if strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "GITHUB_") {
		return github
	}
	// Claude Code's own CLAUDE_CODE_ variables, when the agent runs under
	// it, are about that session — its id, its socket — not this one's.
	if strings.HasPrefix(k, "CLAUDE_CODE_") {
		return k == "CLAUDE_CODE_OAUTH_TOKEN" || k == "CLAUDE_CODE_USE_BEDROCK" || k == "CLAUDE_CODE_USE_VERTEX"
	}
	for _, p := range []string{"ANTHROPIC_", "PI_", "OLLAMA_", "OPENAI_", "VENICE_", "OPENROUTER_", "GEMINI_", "GOOGLE_API"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	for _, s := range []string{"_API_KEY", "_TOKEN", "_BASE_URL"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	switch k {
	case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "TZ", "LANG":
		return true
	}
	return false
}

// command is the harness's process, run in the cage.
func (r *cageRun) command(args []string) (string, []string) {
	return r.c.Podman, append([]string{"exec", "-i", "-w", r.dir, "--env-file", r.envFile, r.container, r.bin}, args...)
}

// stop stops the cage once its harness's process has ended: what the session
// left running ends with it.
func (r *cageRun) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.c.run(ctx, "stop", "-t", "3", r.container)
}

// remove deletes a session's cage, and what was installed in it.
func (c *Cages) remove(container string, stateDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c.run(ctx, "rm", "-f", "-t", "3", container)
	os.Remove(filepath.Join(stateDir, "cages", container+".env"))
}

// busy reports a command still running in the cage besides the harness and
// its MCP servers — a background shell, which ends a turn but not the work.
func (c *Cages) busy(container, harnessBin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := c.run(ctx, "top", container, "args")
	if err != nil {
		return false
	}
	return busyIn(out, harnessBin)
}

func busyIn(top, harnessBin string) bool {
	lines := strings.Split(strings.TrimSpace(top), "\n")
	for _, l := range lines[min(1, len(lines)):] {
		l = strings.TrimSpace(l)
		switch {
		case l == "", strings.HasPrefix(l, "sleep infinity"), strings.Contains(l, "mcp"),
			harnessBin != "" && strings.HasPrefix(l, harnessBin), strings.HasPrefix(l, filepath.Base(harnessBin)+" "):
			continue
		}
		return true
	}
	return false
}

// SetCage puts a session in a cage, changes its cage, or takes it out of one
// (cage nil). Not while it works or a prompt waits: its process is stopped,
// and the next message starts it where it now runs, on the same
// conversation. The cage it had is deleted, and what was installed in it.
func (m *Manager) SetCage(name string, cage *Cage, by string) (Info, error) {
	s, ok := m.Get(name)
	if !ok {
		return Info{}, fmt.Errorf("no session called %q", name)
	}
	if cage != nil && m.Cages == nil {
		return Info{}, errors.New("this machine has no podman to cage sessions with")
	}
	if cage != nil && cage.Sealed {
		if err := m.canSeal(s.harness.Name(), s.dir); err != nil {
			return Info{}, err
		}
	}
	if in := s.Info(); in.State != Idle || in.Pending > 0 {
		return Info{}, fmt.Errorf("session %s is %s: move it once it is idle", name, in.State)
	}
	s.stop()
	s.mu.Lock()
	old := s.cage
	if cage != nil {
		cage = newCage(cage, s.Name())
	}
	s.cage = cage
	data := map[string]any{"caged": cage != nil}
	if cage != nil {
		data["image"] = m.Cages.image(cage)
		data["nix"], data["github"], data["shared"], data["sealed"] = cage.Nix, cage.GitHub, cage.Shared, cage.Sealed
	}
	s.record("caged", by, data)
	s.mu.Unlock()
	m.mu.Lock()
	err := m.save()
	m.mu.Unlock()
	if old != nil && m.Cages != nil {
		m.stopCageProxy(old.Container)
		go m.Cages.remove(old.Container, m.dir)
	}
	if cage != nil {
		m.Cages.Prepare(m.Cages.image(cage), m.log.Info)
	}
	return s.Info(), err
}

var (
	cageMemoryRe = regexp.MustCompile(`^[1-9][0-9]{0,6}[mMgG]$`)
	cageCPUsRe   = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,2})?$`)
)

// SetCageLimits sets a caged session's memory and CPUs; "" goes back to the
// machine's. A cage that exists is changed where it is (podman update), not
// made again: what was installed in it (a nix, a toolchain) stays. One that
// does not exist yet is made with them.
func (m *Manager) SetCageLimits(name, memory, cpus, by string) (Info, error) {
	s, ok := m.Get(name)
	if !ok {
		return Info{}, fmt.Errorf("no session %s", name)
	}
	memory, cpus = strings.TrimSpace(memory), strings.TrimSpace(cpus)
	if memory != "" && !cageMemoryRe.MatchString(memory) {
		return Info{}, fmt.Errorf("%q is not a memory limit: say e.g. 16g or 512m", memory)
	}
	if cpus != "" {
		n, err := strconv.ParseFloat(cpus, 64)
		if !cageCPUsRe.MatchString(cpus) || err != nil || n <= 0 || n > float64(runtime.NumCPU()) {
			return Info{}, fmt.Errorf("%q is not a number of CPUs between 0 and %d, this machine's", cpus, runtime.NumCPU())
		}
	}
	s.mu.Lock()
	if s.cage == nil {
		s.mu.Unlock()
		return Info{}, fmt.Errorf("session %s is not caged", name)
	}
	s.cage.Memory, s.cage.CPUs = memory, cpus
	container := s.cage.Container
	mem, cpu := m.Cages.limits(s.cage)
	s.record("setting", by, map[string]string{"cage_memory": mem, "cage_cpus": cpu})
	s.mu.Unlock()
	m.mu.Lock()
	err := m.save()
	m.mu.Unlock()
	if err != nil {
		return Info{}, err
	}
	if m.Cages != nil {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		if _, err := m.Cages.run(ctx, "container", "exists", container); err == nil {
			args := []string{"update"}
			if mem != "" {
				args = append(args, "--memory", mem)
			}
			if cpu != "" {
				args = append(args, "--cpus", cpu)
			}
			if len(args) > 1 {
				if _, err := m.Cages.run(ctx, append(args, container)...); err != nil {
					return s.Info(), fmt.Errorf("kept for its next cage, but its cage now: %w", err)
				}
			}
		}
	}
	return s.Info(), nil
}
