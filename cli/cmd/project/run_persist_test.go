package project

// run_persist_test.go: `drift file run --persist` keeps one Backbone key per project.
//
// The runner image encrypts what Backbone stores with the key in
// BACKBONE_ENCRYPTION_KEY, 64 hexadecimal characters, and makes a random one at
// every start when the variable is absent. A volume kept across runs is then
// unreadable at the next run. So the key is made once, kept beside the project
// and handed to every run of that project. It is never in the Driftfile and never
// in anything the command prints. A test here never prints a key either: a
// failure shows an argument list with the key masked.

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const keyEnvName = "BACKBONE_ENCRYPTION_KEY"

var sixtyFourHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// keysIn is the value of every `-e BACKBONE_ENCRYPTION_KEY=<value>` pair in an
// argument list.
func keysIn(args []string) []string {
	var keys []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" {
			continue
		}
		if value, ok := strings.CutPrefix(args[i+1], keyEnvName+"="); ok {
			keys = append(keys, value)
		}
	}
	return keys
}

// masked is an argument list with the value of the key replaced, for a failure
// message.
func masked(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, keyEnvName+"=") {
			a = keyEnvName + "=<masked>"
		}
		out[i] = a
	}
	return out
}

// projectIn is a manifest whose Driftfile directory is a fresh, empty directory.
func projectIn(t *testing.T) *Manifest {
	t.Helper()
	return manifestRooted(Node{"slice": "lab"}, t.TempDir())
}

func mustLocalRunArgs(t *testing.T, m *Manifest, container, image string, port int, persist bool, quotaJSON string) []string {
	t.Helper()
	args, err := localRunArgs(m, container, image, port, persist, quotaJSON)
	if err != nil {
		t.Fatalf("localRunArgs: %v", err)
	}
	return args
}

// theKey is the one key a persisted run of m is handed.
func theKey(t *testing.T, m *Manifest) string {
	t.Helper()
	args := mustLocalRunArgs(t, m, "drift-lab", "drift-run-lab", 8002, true, "")
	keys := keysIn(args)
	if len(keys) != 1 {
		t.Fatalf("a persisted run must be handed exactly one -e %s=<key>, got %d in %v", keyEnvName, len(keys), masked(args))
	}
	return keys[0]
}

// filesHolding lists, relative to root, the files under it whose contents
// include text.
func filesHolding(t *testing.T, root, text string) []string {
	t.Helper()
	var held []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), text) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			held = append(held, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// keyFileOf is the path of the one file under m's directory that holds key.
func keyFileOf(t *testing.T, m *Manifest, key string) string {
	t.Helper()
	held := filesHolding(t, m.BaseDir(), key)
	if len(held) != 1 {
		t.Fatalf("the key must be kept in exactly one file beside the project, found %d", len(held))
	}
	return filepath.Join(m.BaseDir(), held[0])
}

// outputOf runs fn and returns everything it wrote to stdout and stderr.
func outputOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			os.Stdout, os.Stderr = stdout, stderr
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// ─── the argument list ──────────────────────────────────────────────────────

// The container is handed a key of 64 hexadecimal characters.
func TestLocalRunArgs_PersistHandsTheContainerAKeyOf64HexCharacters(t *testing.T) {
	key := theKey(t, projectIn(t))

	if !sixtyFourHex.MatchString(key) {
		t.Errorf("the key is %d characters long, want 64 lower-case hexadecimal characters", len(key))
	}
}

// A re-run is handed the key the first run was. The manifest is read afresh for
// the second run, as a second `drift file run` reads it, so the key has to come
// from where it is kept and not from memory.
func TestLocalRunArgs_PersistHandsTheNextRunTheSameKey(t *testing.T) {
	dir := t.TempDir()

	first := theKey(t, manifestRooted(Node{"slice": "lab"}, dir))
	second := theKey(t, manifestRooted(Node{"slice": "lab"}, dir))

	if first != second {
		t.Error("the second run was handed a different key, so it cannot read what the first stored")
	}
}

// Two projects do not share a key.
func TestLocalRunArgs_PersistHandsAnotherProjectAnotherKey(t *testing.T) {
	if theKey(t, projectIn(t)) == theKey(t, projectIn(t)) {
		t.Error("two projects were handed the same key")
	}
}

// The control: a run that keeps no data has no key to keep, and writes nothing
// beside the project.
func TestLocalRunArgs_WithoutPersistThereIsNoKeyAndNothingIsWritten(t *testing.T) {
	m := projectIn(t)

	args := mustLocalRunArgs(t, m, "drift-lab", "drift-run-lab", 8002, false, "")

	for _, a := range args {
		if strings.Contains(a, keyEnvName) {
			t.Errorf("a run without --persist was handed %s", keyEnvName)
		}
	}
	entries, err := os.ReadDir(m.BaseDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a run without --persist wrote %d entries beside the project", len(entries))
	}
}

// ─── where the key is kept ──────────────────────────────────────────────────

// The key is in one file beside the project, in a directory of its own that only
// its owner can enter, and the file is readable and writable by its owner alone.
// It is not in the user's home directory, where another project would find it.
func TestPersistedKey_IsKeptBesideTheProjectReadableOnlyByItsOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := projectIn(t)
	key := theKey(t, m)

	path := keyFileOf(t, m, key)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("the key file is %v, want -rw-------", got)
	}
	dir := filepath.Dir(path)
	if dir == m.BaseDir() {
		t.Fatal("the key file lies loose in the project directory; it belongs in a directory of its own")
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("the directory holding the key is %v, want no access for the group or others", got)
	}
	if elsewhere := filesHolding(t, home, key); len(elsewhere) != 0 {
		t.Errorf("the key is also kept under the home directory, in %d files", len(elsewhere))
	}
}

// The directory that holds the key ignores itself, so a `git add .` in the
// project cannot take the key with it.
func TestPersistedKey_TheDirectoryHoldingItIsIgnoredByGit(t *testing.T) {
	m := projectIn(t)
	path := keyFileOf(t, m, theKey(t, m))

	ignore, err := os.ReadFile(filepath.Join(filepath.Dir(path), ".gitignore"))
	if err != nil {
		t.Fatalf("the directory holding the key has no .gitignore: %v", err)
	}
	if strings.TrimSpace(string(ignore)) != "*" {
		t.Errorf("the .gitignore beside the key says %q, want a single * line", ignore)
	}
}

// A key file that is not a key is refused, and left as it is. A fresh key beside
// the data the old one encrypted would make everything the volume holds
// unreadable, and what the file holds is never printed in the refusal.
func TestLocalRunArgs_PersistRefusesAKeyFileThatIsNotAKeyAndLeavesItAlone(t *testing.T) {
	m := projectIn(t)
	key := theKey(t, m)
	path := keyFileOf(t, m, key)
	damaged := key[:63]
	if err := os.WriteFile(path, []byte(damaged+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args, err := localRunArgs(m, "drift-lab", "drift-run-lab", 8002, true, "")

	if err == nil {
		t.Fatalf("a key file of 63 characters was accepted: %v", masked(args))
	}
	if strings.Contains(err.Error(), damaged) {
		t.Error("the refusal prints what the key file holds")
	}
	if keys := keysIn(args); len(keys) != 0 {
		t.Error("a container was handed a key although the key file was refused")
	}
	if got, _ := os.ReadFile(path); string(got) != damaged+"\n" {
		t.Error("the refused key file was rewritten")
	}
}

// A project directory that cannot hold the key refuses the run instead of
// starting a container with a key nothing keeps.
func TestLocalRunArgs_PersistRefusesWhenTheKeyCannotBeKept(t *testing.T) {
	notADirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	args, err := localRunArgs(manifestRooted(Node{"slice": "lab"}, notADirectory),
		"drift-lab", "drift-run-lab", 8002, true, "")

	if err == nil {
		t.Fatalf("a project directory that cannot hold the key was accepted: %v", masked(args))
	}
	if keys := keysIn(args); len(keys) != 0 {
		t.Error("a container was handed a key although nothing keeps it")
	}
}

// A manifest that knows no directory has nowhere to keep a key. It is refused,
// and nothing is written into the working directory in its place.
func TestLocalRunArgs_PersistWithoutAProjectDirectoryIsRefusedAndWritesNothing(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	_, err := localRunArgs(manifestFrom(Node{"slice": "lab"}), "drift-lab", "drift-run-lab", 8002, true, "")

	if err == nil {
		t.Error("a manifest with no project directory was handed a key")
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("%d entries were written into the working directory", len(entries))
	}
}

// The flag says where the key is, so nobody finds it by accident or loses it by
// cleaning the project.
func TestRunCmd_PersistHelpSaysTheKeyLivesBesideTheProject(t *testing.T) {
	flag := getRunCmd().Flags().Lookup("persist")
	if flag == nil {
		t.Fatal("`drift file run` has no --persist")
	}
	if !strings.Contains(flag.Usage, "beside the project") {
		t.Errorf("--persist says %q, want it to say the key lives beside the project", flag.Usage)
	}
}

// ─── the command ────────────────────────────────────────────────────────────

// fakeDocker puts a `docker` on PATH that records the arguments of every call,
// one line a call, and succeeds. While FAKE_DOCKER_FAIL_CREATE is set, a create
// fails instead and says its own arguments, as a daemon that quotes the command
// it refused would. It returns the path of the record.
func fakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + record + "'\n" +
		"if [ \"$1\" = create ] && [ -n \"$FAKE_DOCKER_FAIL_CREATE\" ]; then\n" +
		"  echo \"docker: refused: $*\" >&2\n  exit 1\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil { // #nosec G306 -- a test's own executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

var (
	dockerCreate = regexp.MustCompile(`(?m)^create .*$`)
	createdKey   = regexp.MustCompile(` -e ` + keyEnvName + `=([0-9a-f]{64})( |$)`)
)

// rigged sets up what `drift file run` needs to run a one-slice project in a unit
// test: the platform's answers, the Driftfile schema, the project as the working
// directory, a recorder in place of the Docker daemon and a server in place of
// the container. It returns the path of the record of docker's calls and a
// function that runs the command with the flags it is given and returns
// everything the command printed and how it ended.
func rigged(t *testing.T) (record string, run func(flags ...string) (string, error)) {
	t.Helper()
	quotaStub(t, http.StatusOK, sliceQuotaBody)
	withRealSchema(t)
	applyIn(t, "slice \"lab\" {}\n")
	record = fakeDocker(t)
	container := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(container.Close)
	served, err := url.Parse(container.URL)
	if err != nil {
		t.Fatal(err)
	}
	return record, func(flags ...string) (string, error) {
		var err error
		out := outputOf(t, func() {
			cmd := getRunCmd()
			cmd.SetArgs(append([]string{"--no-env-file", "--port", served.Port()}, flags...))
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err = cmd.Execute()
		})
		return out, err
	}
}

// `drift file run --persist` hands `docker create` the key on every run, the same
// one, and prints it nowhere: not while it builds, not when it reports the
// container. The Docker daemon is replaced by a recorder, and the container by a
// server that answers.
func TestRunPersist_TheKeyReachesDockerOnEveryRunAndIsInNothingTheCommandPrints(t *testing.T) {
	record, run := rigged(t)
	succeeds := func(flags ...string) string {
		t.Helper()
		out, err := run(flags...)
		if err != nil {
			t.Fatalf("drift file run %s: %v", strings.Join(flags, " "), err)
		}
		return out
	}

	firstOutput := succeeds("--persist")
	secondOutput := succeeds("--persist")
	plainOutput := succeeds()

	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	creates := dockerCreate.FindAllString(string(calls), -1)
	if len(creates) != 3 {
		t.Fatalf("docker create ran %d times, want 3: two persisted runs and one without --persist", len(creates))
	}
	keyOf := func(line string) string {
		if m := createdKey.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		return ""
	}
	first, second := keyOf(creates[0]), keyOf(creates[1])
	if first == "" {
		t.Fatalf("the first persisted run's docker create was not handed -e %s=<64 hexadecimal characters>", keyEnvName)
	}
	if second != first {
		t.Error("the second persisted run was handed a different key")
	}
	if strings.Contains(creates[2], keyEnvName) {
		t.Error("a run without --persist was handed a key")
	}
	if n := strings.Count(string(calls), first); n != 2 {
		t.Errorf("the key reached docker %d times, want 2: once on each persisted run's create and nowhere else", n)
	}
	for who, printed := range map[string]string{
		"the first persisted run":  firstOutput,
		"the second persisted run": secondOutput,
		"a run without --persist":  plainOutput,
	} {
		if strings.Contains(printed, first) {
			t.Errorf("%s printed the key", who)
		}
	}
}

// A daemon that quotes the command it refused would put the key into the error
// the command returns. The command takes it out and still says the create was
// refused, so the run does not read as a success.
func TestRunPersist_ARefusedCreateDoesNotCarryTheKeyIntoTheError(t *testing.T) {
	record, run := rigged(t)
	t.Setenv("FAKE_DOCKER_FAIL_CREATE", "1")

	out, err := run("--persist")

	if err == nil {
		t.Fatal("a run whose docker create was refused reported success")
	}
	calls, readErr := os.ReadFile(record)
	if readErr != nil {
		t.Fatal(readErr)
	}
	handed := createdKey.FindStringSubmatch(string(calls))
	if handed == nil {
		t.Fatalf("docker create was not handed -e %s=<64 hexadecimal characters>", keyEnvName)
	}
	if !strings.Contains(err.Error(), "docker create failed") {
		t.Error("the error does not say the create was refused")
	}
	if strings.Contains(err.Error(), handed[1]) {
		t.Error("the error carries the key the daemon quoted")
	}
	if strings.Contains(out, handed[1]) {
		t.Error("the command printed the key the daemon quoted")
	}
}
