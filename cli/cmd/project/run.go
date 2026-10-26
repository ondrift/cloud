package project

// run.go — `drift file run`: build the project's functions + canvas locally,
// bake them into a thin image on top of ondrift/runner, and launch a detached
// container that boot-scans the layout and serves the app on localhost. The
// "Run it locally" half of the two-button promise (Host on Drift · Run it
// locally). No control plane, no account.
//
// Design: bake-the-code (immutable artifacts → an image layer, so the temp dir
// is deleted immediately and nothing on the host is tethered to the background
// container) + volume-the-data (Backbone LMDB → a named volume so a redeploy
// keeps your data) — see docs/memos/todo/self-host-slice-docker-run.md.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	"github.com/ondrift/cloud/cli/common"

	"github.com/spf13/cobra"
)

// runnerImage is the lean single-app runtime the thin per-app image is built
// FROM. Overridable for local development against an unpublished build.
func runnerImage() string {
	if v := os.Getenv("DRIFT_RUNNER_IMAGE"); v != "" {
		return v
	}
	return "ondrift/runner:latest"
}

func getRunCmd() *cobra.Command {
	var (
		envName   string
		hostPort  int
		persist   bool
		noEnvFile bool
	)
	cmd := &cobra.Command{
		Use:   "run [environment]",
		Short: "Build and run the project locally in Docker (no account, no cloud)",
		Long: `Build the project's functions, workers and canvas, bake a thin image on top
of the Drift runtime, and launch it detached. The app is served on localhost,
and its workers run beside it: the same code that runs on Drift Cloud, on your
own machine.

Needs Docker — and only Docker. Every language builds in a throwaway container
(Go, Rust, Python, Node, PHP, Ruby), so you install no toolchain at all. Set
DRIFT_RUN_HOST_BUILD=1 to use your host toolchains instead (faster, no pulls).`,
		Example: "  drift file run\n  drift file run --port 9000\n  drift file run --persist",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selectedEnv := envName
			if len(args) == 1 {
				selectedEnv = args[0]
			}
			app, container, url, err := startLocal(selectedEnv, hostPort, persist, noEnvFile)
			if err != nil {
				return err
			}
			fmt.Printf("\n  %s %s running (container %s)\n", common.Check(), common.Highlight(app), container)
			fmt.Printf("     → %s\n", common.Highlight(url))
			fmt.Printf("     %s\n\n", common.Hint("drift file logs · drift file stop"))
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to run (same as the positional argument)")
	cmd.Flags().IntVar(&hostPort, "port", 0, "Host port to map Canvas (:8002) to (default: 8002, or the next free port)")
	cmd.Flags().BoolVar(&persist, "persist", false, "Keep Backbone data in a named volume across runs")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file")
	return cmd
}

// startLocal is `drift file run`'s actual work, extracted so `drift file test`
// can start the same local instance, run tests against it, and tear it down —
// without duplicating the build → bake → launch → health-poll sequence. Returns
// the app name, container name, and base URL of the now-running (and confirmed
// healthy) instance.
func startLocal(selectedEnv string, hostPort int, persist, noEnvFile bool) (app, container, url string, err error) {
	if err := requireDocker(); err != nil {
		return "", "", "", err
	}
	manifestPath, err := requireDriftfile()
	if err != nil {
		return "", "", "", err
	}
	projectDir := filepath.Dir(manifestPath)

	// Same variable origin hierarchy as `drift apply`: ENV bound to the
	// environment, then .env.<env> and .env, so env() and hook shells resolve
	// identically.
	overrides := []string{}
	if selectedEnv != "" {
		overrides = append(overrides, "ENV="+selectedEnv)
	}
	vars, err := applyVariableSources(projectDir, overrides, !noEnvFile, selectedEnv)
	if err != nil {
		return "", "", "", err
	}
	vars.report()

	// pre_deploy hooks first (e.g. a frontend build that produces the
	// canvas dir the parse then validates).
	preHooks, _, err := ParseHooks(manifestPath)
	if err != nil {
		return "", "", "", err
	}
	if err := runHooks("pre_deploy", preHooks, projectDir); err != nil {
		return "", "", "", err
	}

	m, err := ParseDriftfileEnv(manifestPath, selectedEnv)
	if err != nil {
		return "", "", "", err
	}
	app = m.Name()
	container = "drift-" + app

	// ── Build into a temp slot layout ───────────────────────────────
	work, err := os.MkdirTemp("", "drift-run-"+app+"-")
	if err != nil {
		return "", "", "", err
	}
	defer os.RemoveAll(work) // baked into the image below; nothing tethered
	runnerDir := filepath.Join(work, "runner")
	canvasDir := filepath.Join(work, "canvas")

	fmt.Printf("\n  %s building %s…\n", common.Hint("·"), common.Highlight(app))
	// Functions and Workers alike: the runner image is a slice, and its supervisor
	// starts every Worker slot it finds, so a local run behaves as the cloud does.
	backends, err := atomic_cmd.BuildBackends(AtomicSpecs(m))
	if err != nil {
		return "", "", "", err
	}
	if len(backends) > 0 {
		if err := checkRouteCollisions(m); err != nil {
			return "", "", "", err
		}
		if err := atomic_cmd.StageBackendsLocally(backends, runnerDir, true); err != nil {
			return "", "", "", fmt.Errorf("build functions and workers: %w", err)
		}
	} else {
		_ = os.MkdirAll(runnerDir, 0o755)
	}
	if err := layoutCanvas(m, canvasDir); err != nil {
		return "", "", "", fmt.Errorf("lay out canvas: %w", err)
	}

	// ── Bake the thin image (FROM ondrift/runner, COPY layout) ──────
	fmt.Printf("  %s baking image…\n", common.Hint("·"))
	image := "drift-run-" + app
	if err := bakeImage(work, image); err != nil {
		return "", "", "", err
	}

	// ── Launch detached ─────────────────────────────────────────────
	_ = exec.Command("docker", "rm", "-f", container).Run() // re-run = replace
	port := hostPort
	if port == 0 {
		port = pickPort(8002)
	}
	// The limits the real slice enforces, so what is refused there is refused
	// here. Read before the container starts and forwarded unparsed; see
	// quota.go for why the CLI does not build this document itself.
	quotaJSON := localQuotaConfig(app, os.Stdout)
	runArgs := localRunArgs(m, container, image, port, persist, quotaJSON)
	if out, err := exec.Command("docker", runArgs...).CombinedOutput(); err != nil {
		return "", "", "", fmt.Errorf("docker create failed: %s", string(out))
	}
	// The declared queues exist before the slice starts, as `drift apply` makes
	// them exist on a slice before any slot is deployed.
	if err := seedDeclaredQueues(container, image, DeclaredQueues(m)); err != nil {
		_ = exec.Command("docker", "rm", "-f", container).Run()
		return "", "", "", err
	}
	if out, err := exec.Command("docker", "start", container).CombinedOutput(); err != nil {
		return "", "", "", fmt.Errorf("docker start failed: %s", string(out))
	}

	// ── Health-poll so the closeout is true, not hopeful ────────────
	url = fmt.Sprintf("http://127.0.0.1:%d/", port)
	if !waitHealthy(url, 8*time.Second) {
		logs, _ := exec.Command("docker", "logs", "--tail", "20", container).CombinedOutput()
		return "", "", "", fmt.Errorf("%s started but isn't responding on %s — last log lines:\n%s",
			app, url, string(logs))
	}
	return app, container, url, nil
}

// localRunArgs builds the `docker create` argv for one local instance. The
// container is created, seeded and then started, so the declared queues are in
// its data directory before the slice boots.
//
// Split out of startLocal so what the container is handed can be asserted
// without a Docker daemon. An environment variable either reaches the container
// or silently does not, and this list is the only place that difference is
// visible before the process exists.
func localRunArgs(m *Manifest, container, image string, port int, persist bool, quotaJSON string) []string {
	args := []string{
		"create", "--name", container,
		"-p", fmt.Sprintf("127.0.0.1:%d:8002", port), // canvas only; :8000/:8001 stay internal
		"-e", "DRIFT_STANDALONE_SAT=drift-run",
	}

	// Omitted rather than passed empty when there are no limits to hand over.
	// The slice reads an empty value and a missing key identically, so this is
	// for the reader of `docker inspect`: no variable says "nothing was known",
	// where an empty one looks like a slice that declared nothing.
	if quotaJSON != "" {
		args = append(args, "-e", "DRIFT_QUOTA_CONFIG="+quotaJSON)
	}

	// Declared secrets ride in as DRIFT_SECRET_<NAME>; the slice seeds them
	// into its AES-encrypted store at boot (standalone only) and the runner
	// then injects each declared secret into its function. Values are already
	// resolved from their env() references. Docker-native `-e SECRET=…` — no
	// admin port exposed.
	// (Visible to `docker inspect` on this host, the user's own machine; the
	// SAT itself is never passed this way.)
	for name, val := range m.Slice().Sub("backbone", "secrets") {
		args = append(args, "-e", fmt.Sprintf("DRIFT_SECRET_%s=%s", name, val))
	}

	if persist {
		args = append(args, "-v", container+"-data:/data")
	}
	return append(args, image)
}

// seedDeclaredQueues writes one directory per declared queue into the created,
// not yet started, container's Backbone data directory. The slice restores each
// as an existing empty queue at boot, so a declared queue nothing has pushed to
// reads empty, while an undeclared one is still the slice's 404.
//
// The data directory and the owner come from the image rather than from here:
// the runner image owns BACKBONE_DATA_DIR and the user the slice runs as, and
// a directory the slice cannot write to would refuse the queue's first push.
// Directories alone, so a persisted volume keeps every message it holds.
func seedDeclaredQueues(container, image string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	dataDir, uid, gid, err := imageDataDirAndUser(image)
	if err != nil {
		return err
	}
	archive, err := atomic_cmd.QueueDirsArchive(names, uid, gid)
	if err != nil {
		return err
	}
	cp := exec.Command("docker", "cp", "--archive", "-", container+":"+dataDir)
	cp.Stdin = bytes.NewReader(archive)
	if out, err := cp.CombinedOutput(); err != nil {
		return fmt.Errorf("create the declared queues in %s: %s", container, string(out))
	}
	return nil
}

// imageDataDirAndUser reads BACKBONE_DATA_DIR and the numeric user the image
// runs as.
func imageDataDirAndUser(image string) (dir string, uid, gid int, err error) {
	out, err := exec.Command("docker", "image", "inspect", "--format",
		"{{json .Config.Env}} {{json .Config.User}}", image).Output()
	if err != nil {
		return "", 0, 0, fmt.Errorf("inspect %s: %w", image, err)
	}
	return parseImageDataDirAndUser(string(out))
}

// parseImageDataDirAndUser reads `docker image inspect`'s `<env json> <user json>`.
func parseImageDataDirAndUser(inspected string) (dir string, uid, gid int, err error) {
	envJSON, userJSON, _ := strings.Cut(strings.TrimSpace(inspected), " ")
	var env []string
	var user string
	if json.Unmarshal([]byte(envJSON), &env) != nil || json.Unmarshal([]byte(userJSON), &user) != nil {
		return "", 0, 0, fmt.Errorf("unreadable image config: %q", inspected)
	}
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "BACKBONE_DATA_DIR="); ok {
			dir = v
		}
	}
	if dir == "" {
		return "", 0, 0, fmt.Errorf("the runner image sets no BACKBONE_DATA_DIR, so there is nowhere to create the declared queues")
	}
	u, g, _ := strings.Cut(user, ":")
	if uid, err = strconv.Atoi(u); err != nil {
		return "", 0, 0, fmt.Errorf("the runner image runs as %q, not a numeric uid:gid", user)
	}
	gid = uid
	if g != "" {
		if gid, err = strconv.Atoi(g); err != nil {
			return "", 0, 0, fmt.Errorf("the runner image runs as %q, not a numeric uid:gid", user)
		}
	}
	return dir, uid, gid, nil
}

func getStopCmd() *cobra.Command {
	var purge bool
	var envName string
	cmd := &cobra.Command{
		Use:   "stop [environment]",
		Short: "Stop the local container started by 'drift file run'",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireDocker(); err != nil {
				return err
			}
			selectedEnv := envName
			if len(args) == 1 {
				selectedEnv = args[0]
			}
			app, err := manifestAppName(selectedEnv)
			if err != nil {
				return err
			}
			container := "drift-" + app
			if out, err := exec.Command("docker", "rm", "-f", container).CombinedOutput(); err != nil {
				return fmt.Errorf("stop %s: %s", container, string(out))
			}
			fmt.Printf("  %s stopped %s\n", common.Check(), container)
			if purge {
				_ = exec.Command("docker", "volume", "rm", container+"-data").Run()
				fmt.Printf("  %s removed data volume %s-data\n", common.Check(), container)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Also remove the persisted data volume")
	cmd.Flags().StringVar(&envName, "env", "", "Environment whose container to target (same as the positional argument)")
	return cmd
}

func getLogsCmd() *cobra.Command {
	var follow bool
	var envName string
	cmd := &cobra.Command{
		Use:   "logs [environment]",
		Short: "Show logs for the local container started by 'drift file run'",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireDocker(); err != nil {
				return err
			}
			selectedEnv := envName
			if len(args) == 1 {
				selectedEnv = args[0]
			}
			app, err := manifestAppName(selectedEnv)
			if err != nil {
				return err
			}
			a := []string{"logs"}
			if follow {
				a = append(a, "-f")
			}
			a = append(a, "drift-"+app)
			c := exec.Command("docker", a...)
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().StringVar(&envName, "env", "", "Environment whose container to target (same as the positional argument)")
	return cmd
}

// ─── helpers ────────────────────────────────────────────────────────────────

func requireDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("`drift file run` needs Docker — install Docker Desktop or the engine and try again")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return fmt.Errorf("Docker is installed but the daemon isn't reachable — is Docker running?")
	}
	return nil
}

func manifestAppName(env string) (string, error) {
	mp, err := requireDriftfile()
	if err != nil {
		return "", err
	}
	// Cheap name-only parse — `stop`/`logs` must not require the project's
	// secrets to be set just to find the container. It names the environment's
	// slice exactly as the compile `run` makes does, so the names always match.
	return ParseProjectName(mp, env)
}

// layoutCanvas writes the slice's on-disk canvas format under dir:
// <dir>/<slug>/ per site + registry.json mapping slug → route.
func layoutCanvas(m *Manifest, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	type siteEntry struct {
		Slug  string `json:"slug"`
		Route string `json:"route"`
	}
	sites, err := canvasSites(m)
	if err != nil {
		return err
	}
	var registry []siteEntry
	for _, s := range sites {
		if err := copyTree(m.ResolvePath(s.Dir), filepath.Join(dir, s.Slug)); err != nil {
			return err
		}
		registry = append(registry, siteEntry{Slug: s.Slug, Route: s.Route})
	}
	b, _ := json.Marshal(registry)
	return os.WriteFile(filepath.Join(dir, "registry.json"), b, 0o644)
}

// thinDockerfile is the Dockerfile bakeImage builds: the runner image plus the
// project's functions and canvas.
//
// The functions go where the runner image's own RUNNER_DIR says, and nowhere
// else. The sandbox's Landlock rules let a function execute only beneath the
// runner paths the platform mounts, so code copied anywhere else is refused at
// its first invocation: the wrapper's execve fails, the worker exits 65, and the
// caller sees "warm worker closed its output". The image owns RUNNER_DIR,
// CANVAS_DATA_DIR and BACKBONE_DATA_DIR, so this file names the variables and
// sets none of them.
func thinDockerfile(base string) string {
	return fmt.Sprintf(`FROM %s
COPY --chmod=a+rX runner $RUNNER_DIR
COPY --chmod=a+rX canvas $CANVAS_DATA_DIR
`, base)
}

// bakeImage writes a thin Dockerfile in work and builds <image> from it.
func bakeImage(work, image string) error {
	dockerfile := thinDockerfile(runnerImage())
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return err
	}
	out, err := exec.Command("docker", "build", "-q", "-t", image, work).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build failed: %s", string(out))
	}
	return nil
}

// waitHealthy polls url until it answers (any HTTP status) or the deadline.
func waitHealthy(url string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	client := &http.Client{Timeout: 1 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body) // #nosec G104
			resp.Body.Close()              // #nosec G104
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// pickPort returns preferred if free, else the next free port above it.
func pickPort(preferred int) int {
	for p := preferred; p < preferred+50; p++ {
		if portFree(p) {
			return p
		}
	}
	return preferred
}

func portFree(p int) bool {
	resp, err := (&http.Client{Timeout: 200 * time.Millisecond}).Get(fmt.Sprintf("http://127.0.0.1:%d/", p))
	if err != nil {
		return true // nothing answered → assume free
	}
	resp.Body.Close() // #nosec G104
	return false
}

// copyTree recursively copies src → dst.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("canvas dir %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("canvas path %s is not a directory", src)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path) // #nosec G304 — user's own canvas files
		if err != nil {
			return err
		}
		defer in.Close() // #nosec G307
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.Create(target) // #nosec G304
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close() // #nosec G104
			return err
		}
		return out.Close()
	})
}
