package file

// new.go — scaffold a Driftfile that deploys.
//
// The starter file writes what the format requires (each pulse's handler and
// memory) and comments what it does not, because the gap
// between a file that parses and one that deploys is where a first run stalls.
//
// The scaffold ships one complete pulse rather than an empty element: an empty
// element is refused and teaches nothing, and a user's first edit should be
// changing a real block rather than inventing the shape of one.
//
// # Guided, at a terminal
//
// A person starting a project has no file to edit yet, and `drift apply`
// creates the slice the file names, so the file is where a new slice begins. At
// a terminal this asks the handful of things the platform cannot guess (the
// slice's name, the first pulse's method, route and language, and a folder to serve as the site) and writes the file from the
// answers. Anything given as a flag is not asked, and `--yes` takes every default,
// so a script gets the same file a person gets by pressing Enter.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	atomic_cmd "github.com/ondrift/cloud/cli/cmd/atomic/cmd/deploy"
	atomic_cmd_new "github.com/ondrift/cloud/cli/cmd/atomic/cmd/new"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func getNewCmd() *cobra.Command {
	var (
		opts  starterOptions
		force bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "new [path]",
		Short: "Write a starter Driftfile, asking for the few things it needs",
		Long: "Creates a Driftfile declaring a slice and one pulse, with everything the format " +
			"requires already written, so the first `drift plan` is not a validation error.\n\n" +
			"At a terminal it asks for the slice's name, the first " +
			"pulse's method, route and language, and a folder to serve as the site. A flag answers " +
			"its question in advance, and --yes accepts every default, which is what a script gets.\n\n" +
			"`drift apply` then creates the slice the file names.",
		Example: "  drift file new\n" +
			"  drift file new --name my-app --route orders --method post --lang python --yes\n" +
			"  drift file new ./service --canvas site",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "Driftfile"
			if len(args) > 0 && args[0] != "" {
				target = args[0]
			}
			abs, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			if st, serr := os.Stat(abs); serr == nil && st.IsDir() {
				abs = filepath.Join(abs, "Driftfile")
			}
			if _, serr := os.Stat(abs); serr == nil && !force {
				return fmt.Errorf("%s already exists — pass --force to overwrite it", shortPath(abs))
			}

			// DRIFT_SLICE FIRST, and the directory name only as a fallback.
			//
			// `drift apply` creates the slice a Driftfile names when it does not
			// exist, so a file naming the directory would buy a second slice beside
			// the one the account already made. Naming the slice in DRIFT_SLICE
			// applies the file into that one. A Driftfile further up the tree is
			// deliberately not consulted: a new project inside another is its own.
			chosen := "the directory name"
			if opts.Name == "" {
				if named := strings.TrimSpace(os.Getenv(common.SliceEnv)); named != "" && nameLooksValid(named) {
					opts.Name, chosen = named, common.SliceEnv
				} else {
					opts.Name = filepath.Base(filepath.Dir(abs))
				}
			} else {
				chosen = "--name"
			}

			asked := !yes && term.IsTerminal(int(os.Stdin.Fd()))
			if asked {
				fmt.Println("A Driftfile declares the slice and what runs on it; `drift apply` creates it.")
				fmt.Println("Press Enter to accept the answer in brackets.")
				fmt.Println()
				if err := opts.guide(cmd, chosen, common.PromptForInput); err != nil {
					return err
				}
				chosen = "your answer"
			}
			opts.fillDefaults()
			if err := opts.validate(); err != nil {
				return err
			}

			if werr := os.WriteFile(abs, []byte(opts.render()), 0o600); werr != nil {
				return werr
			}
			// Say WHICH slice, and WHY. The name decides where a deploy lands, and
			// a scaffolder that picks one silently is how the mismatch above went
			// unnoticed until apply refused it.
			fmt.Printf("%s wrote %s — slice %q, from %s\n",
				common.Hint("✓"), shortPath(abs), opts.Name, chosen)
			if chosen == "the directory name" {
				fmt.Printf("  %s\n", common.Hint("named after the directory: pass --name, or edit the slice block by hand"))
			}
			// NAME THE COMMAND THAT WRITES THE SOURCE, because this file declares
			// a pulse that does not exist yet.
			//
			// The scaffolded block is `pulse "GET /hello"` with `handler =
			// "GetHello"`, and nothing on disk defines GetHello — a Driftfile
			// declares pulses, it does not create them. So `drift file new`
			// followed by `drift plan` fails at the element's folder, and the
			// failure names what is missing rather than the command that would
			// produce it. A new user's second command dead-ends on their first.
			//
			// The command below is not an example: it is the one that writes source
			// matching THIS block exactly — same route, same method, and
			// `HandlerName` derives the same `GetHello` from them. The two halves
			// are generated from the same options, so they cannot drift apart
			// while the test that compares them holds.
			fmt.Printf("  %s\n", common.Hint(opts.fnCommand()+" # write the source it declares"))
			fmt.Printf("  %s\n", common.Hint("drift file lint    # check it"))
			fmt.Printf("  %s\n", common.Hint("drift plan         # see what applying it creates, and what it costs"))
			fmt.Printf("  %s\n", common.Hint("drift apply        # create the slice and deploy into it"))
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "", "slice name (default: DRIFT_SLICE, else the directory name)")
	cmd.Flags().StringVar(&opts.Method, "method", "", "the first pulse's HTTP method (default get)")
	cmd.Flags().StringVar(&opts.Route, "route", "", "the first pulse's route under /api/ (default hello)")
	cmd.Flags().StringVar(&opts.Lang, "lang", "", "the first pulse's language: go, python, node, ruby, php or rust (default go)")
	cmd.Flags().StringVar(&opts.Canvas, "canvas", "", "path to a static site directory to serve")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing Driftfile")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask: take a flag's value or the default for everything")
	return cmd
}

// nameLooksValid mirrors the platform's identifier rule. It is a local copy for
// a fast message and nothing more — the parser and the server both enforce it
// again, so this being out of step costs a confusing hint, never a bad deploy.
func nameLooksValid(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

// The ONE pulse the starter Driftfile declares when nothing says otherwise, and
// the only place its default route, method, language and booking are written
// down.
//
// Two things are generated from these values: the pulse block in the
// scaffolded file, and the `drift atomic new` command printed beside it. They
// have to agree — the block names a handler that only that exact command
// produces — and a second copy of "hello"/"get" in either half is how they stop
// agreeing. `TestStarterEntryAndItsCommandAgree` holds them together against the
// real handler-name derivation rather than a restatement of it.
const (
	starterRoute  = "hello"
	starterMethod = "get"
	starterLang   = "go"
	// starterMemory is above the floor a compiled language needs, so the
	// starter's Go pulse is not refused by the language it is written in.
	starterMemory = "32MiB"
)

var (
	starterMethods   = []string{"get", "post", "put", "patch", "delete"}
	starterLanguages = []string{"go", "python", "node", "ruby", "php", "rust"}

	// A route is the path under /api/: the same rule `drift atomic new` and the
	// platform apply, restated here to refuse a typo at the prompt that caused it.
	starterRouteRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_:.-]*$`)
)

// starterOptions are the answers a starter Driftfile is written from.
type starterOptions struct {
	Name, Method, Route, Lang, Canvas string
}

// fillDefaults gives every answer not yet given its default.
func (o *starterOptions) fillDefaults() {
	if o.Method == "" {
		o.Method = starterMethod
	}
	if o.Route == "" {
		o.Route = starterRoute
	}
	if o.Lang == "" {
		o.Lang = starterLang
	}
	o.Method, o.Lang = strings.ToLower(o.Method), strings.ToLower(o.Lang)
}

// validate refuses an answer the file could not be applied with, naming it.
func (o starterOptions) validate() error {
	if !nameLooksValid(o.Name) {
		return fmt.Errorf("project name %q must be 1–32 lowercase letters, numbers or hyphens "+
			"(not starting or ending with one) — pass --name", o.Name)
	}
	if !contains(starterMethods, o.Method) {
		return fmt.Errorf("method %q must be one of %s", o.Method, strings.Join(starterMethods, ", "))
	}
	if !contains(starterLanguages, o.Lang) {
		return fmt.Errorf("language %q must be one of %s", o.Lang, strings.Join(starterLanguages, ", "))
	}
	if strings.HasPrefix(o.Route, "/") {
		return fmt.Errorf("route %q has a leading slash — the route is the path under /api/, so write %q",
			o.Route, strings.TrimLeft(o.Route, "/"))
	}
	if !starterRouteRe.MatchString(o.Route) {
		return fmt.Errorf("route %q may hold letters, numbers and / _ : . - only", o.Route)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// guide asks, at a terminal, for each answer a flag did not already give.
// ask prints a label and returns the line typed; an empty line is the default.
func (o *starterOptions) guide(cmd *cobra.Command, nameFrom string, ask func(label string) string) error {
	pick := func(flag string, into *string, label, def string, check func() error) error {
		if cmd.Flags().Changed(flag) {
			return nil
		}
		for tries := 0; ; tries++ {
			if v := strings.TrimSpace(ask(fmt.Sprintf("%s [%s]", label, def))); v != "" {
				*into = v
			} else {
				*into = def
			}
			o.fillDefaults()
			err := check()
			if err == nil {
				return nil
			}
			if tries >= 2 {
				return err
			}
			fmt.Printf("  %s %v\n", common.Hint("✗"), err)
		}
	}

	if err := pick("name", &o.Name, "Slice name (from "+nameFrom+")", o.Name, func() error {
		if !nameLooksValid(o.Name) {
			return fmt.Errorf("%q must be 1–32 lowercase letters, numbers or hyphens", o.Name)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := pick("method", &o.Method, "First pulse's method ("+strings.Join(starterMethods, "/")+")", starterMethod, func() error {
		if !contains(starterMethods, strings.ToLower(o.Method)) {
			return fmt.Errorf("%q must be one of %s", o.Method, strings.Join(starterMethods, ", "))
		}
		return nil
	}); err != nil {
		return err
	}
	if err := pick("route", &o.Route, "Its route under /api/", starterRoute, func() error {
		return starterOptions{Name: "x", Method: "get", Route: o.Route, Lang: "go"}.validate()
	}); err != nil {
		return err
	}
	if err := pick("lang", &o.Lang, "Its language ("+strings.Join(starterLanguages, "/")+")", starterLang, func() error {
		if !contains(starterLanguages, strings.ToLower(o.Lang)) {
			return fmt.Errorf("%q must be one of %s", o.Lang, strings.Join(starterLanguages, ", "))
		}
		return nil
	}); err != nil {
		return err
	}
	if !cmd.Flags().Changed("canvas") {
		o.Canvas = strings.TrimSpace(ask("Folder to serve as the site, relative to the Driftfile [none]"))
	}
	fmt.Println()
	return nil
}

// fnCommand is the `drift atomic new` invocation that writes source for the
// pulse the Driftfile declares.
func (o starterOptions) fnCommand() string {
	return fmt.Sprintf("drift atomic new %s -l %s -m %s", o.Route, o.Lang, o.Method)
}

// starterFnCommand is fnCommand for the defaults.
func starterFnCommand() string {
	d := starterOptions{Route: starterRoute, Lang: starterLang, Method: starterMethod}
	return d.fnCommand()
}

// starterDriftfile is the file for the defaults, with the given slice name and
// site folder.
func starterDriftfile(name, canvasDir string) string {
	o := starterOptions{Name: name, Canvas: canvasDir}
	o.fillDefaults()
	return o.render()
}

func (o starterOptions) render() string {
	var b strings.Builder
	b.WriteString("# " + o.Name + ": a Drift project.\n")
	b.WriteString("# `drift file explain` lists every block this file can hold, `drift file lint`\n")
	b.WriteString("# checks it without deploying, and `drift plan` shows what applying it does and\n")
	b.WriteString("# what that costs.\n\n")

	b.WriteString("# The file does not say how the slice is paid for: a shape inside the free grant\n")
	b.WriteString("# can be one of your free slices, and `drift plan` says which it is.\n")
	b.WriteString("slice \"" + o.Name + "\" {}\n\n")

	b.WriteString("# The Atomic pillar: every element the slice runs. An element is one folder\n")
	b.WriteString("# of one language's source: \"default\" is ./atomic itself, and any other name\n")
	b.WriteString("# is ./atomic/<name>. Each pulse and field is declared inside the element its\n")
	b.WriteString("# handler lives in; nothing in your source declares one.\n")
	b.WriteString("atomic {\n")
	b.WriteString("  element \"" + atomic_cmd.DefaultElementName + "\" {\n")
	b.WriteString("    # A pulse answers one call with one result. It is labelled with its method\n")
	b.WriteString("    # and route, or with QUEUE and the queue it drains: \"QUEUE orders\".\n")
	b.WriteString("    # Long-running workers are `field` blocks beside it.\n")
	b.WriteString("    pulse \"" + strings.ToUpper(o.Method) + " /" + o.Route + "\" {\n")
	// Derived, never written out. This handler exists only once
	// `fnCommand()` has been run, and that command's own naming rule is the one
	// that decides what it will be called.
	b.WriteString("      handler = \"" + atomic_cmd_new.HandlerName(o.Method, o.Route, o.Lang) + "\"\n")
	b.WriteString("      # The memory it books: its own pool, and what it is billed at.\n")
	b.WriteString("      # `drift size` rewrites it from what the slice measured.\n")
	b.WriteString("      memory = \"" + starterMemory + "\"\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")

	if o.Canvas != "" {
		b.WriteString("\n# The Canvas pillar: every static site the slice serves.\n")
		b.WriteString("canvas {\n")
		b.WriteString("  site \"/\" {\n")
		b.WriteString("    dir = \"" + o.Canvas + "\"\n")
		b.WriteString("  }\n")
		b.WriteString("}\n")
	}

	b.WriteString("\n# An environment is a slice of its own, <slice>-<name> unless it names one:\n")
	b.WriteString("#\n")
	b.WriteString("#   environment \"staging\" {}\n")
	b.WriteString("#\n")
	b.WriteString("# `drift plan staging` and `drift apply staging` work on it.\n")
	return b.String()
}
