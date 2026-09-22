package file

// resolve.go — what THIS Driftfile will actually provision.
//
// The value is the RESOLVED shape, not a re-print of the file. "Omitted means
// inherit" is exactly where users have been getting caught: an environment block
// that sets three knobs looks like it describes the slice, and it describes three
// knobs on top of a base you have to hold in your head. This resolves the
// environment overlay and prints the result, so the answer to "what does staging
// actually get" is a command rather than an inference.
//
// It does NOT print a price. The server is the single authority on cost
// (`plan.PriceConfig`, reached via POST /ops/slice/price) and computing one here
// would be a second source of truth for the number a user is billed — the exact
// thing pricing is centralised to avoid. `drift file apply --plan` shows it,
// with a session, from the server.
//
// # This was `drift file explain`, and the rename is the point
//
// `explain` now describes the FORMAT, kubectl-style — what is this key, what may
// it contain (cloud/public#69). That is a different question from this one, and
// both are useful: one is about the document's grammar, the other about one
// document's meaning.
//
// THEY ARE NOT ALIASES OF EACH OTHER, so there is no alias. cloud-platform#509
// keeps every old route spelling answering forever, and that is safe there
// because each old path reaches the identical handler — the two names mean one
// thing. Here they would mean two, on one verb, which is the ambiguity the split
// exists to remove. `explain` refuses an argument meant for this command and
// names it instead; see the guard in explain.go.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/cmd/project"
	"github.com/ondrift/cloud/cli/common"
	"github.com/spf13/cobra"
)

func getResolveCmd() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "resolve [path]",
		Short: "Show what a Driftfile resolves to, including inherited defaults",
		Long: "Resolves the manifest — applying an environment overlay if one is selected — " +
			"and prints the shape it will provision. Offline; no account needed.\n\n" +
			"For what a KEY means rather than what this file says, see `drift file explain`.",
		Example:      "  drift file resolve\n  drift file resolve --env staging\n  drift file resolve ./Driftfile",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolvePath(args)
			if err != nil {
				return err
			}
			m, perr := project.ParseDriftfile(path)
			if perr != nil {
				return fmt.Errorf("%s\n\n%w", common.Hint(shortPath(path)), perr)
			}

			selected, serr := m.SelectEnvironment(env, cmd.Flags().Changed("env"))
			if serr != nil {
				return serr
			}
			printResolved(m, selected)
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "environment to resolve (default: the project's own default)")
	return cmd
}

func printResolved(m *project.Manifest, env string) {
	s := m.Slice()

	title := fmt.Sprintf("slice %s", m.Name())
	if env != "" {
		title += fmt.Sprintf("  (environment %s)", env)
	}
	fmt.Println(title)
	fmt.Println(strings.Repeat("─", len(title)))

	// Only print what the manifest actually resolves to. A knob left empty is
	// resolved by the platform at deploy time, and printing a guess at that
	// default here would be this command inventing a second answer to
	// "what will I get" — which is the confusion it exists to remove.
	rows := [][2]string{
		{"log retention", s.Str("log_retention")},
		{"backup retention", s.Str("backup_retention")},
		{"function timeout", s.Str("atomic", "function_timeout")},
		{"rate limit", s.Str("atomic", "rate_limit")},
	}
	if dh := s.Int("atomic", "deploy_history"); dh != 0 {
		rows = append(rows, [2]string{"deploy history", fmt.Sprintf("%d", dh)})
	}
	if cs := s.Str("canvas", "canvas_size"); cs != "" {
		rows = append(rows, [2]string{"canvas size", cs})
	}

	fmt.Println()
	for _, r := range rows {
		val := r[1]
		if val == "" {
			val = common.Hint("— platform default")
		}
		fmt.Printf("  %-18s %s\n", r[0], val)
	}

	if fns := s.Entries("name", "atomic", "functions"); len(fns) > 0 {
		fmt.Printf("\n  functions (%d)\n", len(fns))
		for _, f := range fns {
			fmt.Printf("    · %s\n", f.Str("name"))
		}
	}
	if sites := s.Entries("dir", "canvas", "sites"); len(sites) > 0 {
		fmt.Printf("\n  canvas sites (%d)\n", len(sites))
		for _, site := range sites {
			fmt.Printf("    · %s\n", site.Str("dir"))
		}
	}
	if domains := s.Entries("host", "domains"); len(domains) > 0 {
		fmt.Printf("\n  domains (%d)\n", len(domains))
		for _, d := range domains {
			suffix := ""
			if d.Bool("wildcard") {
				suffix = "  (wildcard)"
			}
			fmt.Printf("    · %s%s\n", d.Str("host"), suffix)
		}
	}

	// Secrets: names only, never values. Some of them are resolved $ENVREFs by
	// this point, and a command whose whole job is "print the resolved shape"
	// must not be the thing that puts a live credential on a terminal or into a
	// CI log.
	if secrets := s.Sub("backbone", "secrets"); len(secrets) > 0 {
		names := make([]string, 0, len(secrets))
		for k := range secrets {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Printf("\n  secrets (%d, names only)\n", len(names))
		for _, k := range names {
			fmt.Printf("    · %s\n", k)
		}
	}

	if envs := m.EnvironmentNames(); len(envs) > 0 {
		fmt.Printf("\n  %s %s\n", common.Hint("environments:"), strings.Join(envs, ", "))
	}
	fmt.Printf("\n  %s\n", common.Hint("cost comes from the server — drift file apply --plan"))
}
