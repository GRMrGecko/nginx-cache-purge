package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/alecthomas/kong"
)

// VersionFlag prints build information and exits.
type VersionFlag bool

// Decode satisfies kong.MapperValue. The flag is treated as a boolean toggle.
func (v VersionFlag) Decode(ctx *kong.DecodeContext) error { return nil }

// IsBool reports the flag as a boolean for kong's parser.
func (v VersionFlag) IsBool() bool { return true }

// BeforeApply emits version information then exits before the rest of the
// command is executed.
func (v VersionFlag) BeforeApply(app *kong.Kong, vars kong.Vars) error {
	fmt.Printf("%s: %s (%s)\n", Name, Version, Mode)
	if Commit != "" {
		fmt.Printf("  commit: %s\n", Commit)
	}
	if Date != "" {
		fmt.Printf("  built:  %s\n", Date)
	}
	// Without build stamps, a module-aware build still records the revision
	// it was built from, so fall back to what the toolchain embedded.
	if Commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					fmt.Printf("  commit: %s\n", s.Value)
				case "vcs.time":
					fmt.Printf("  built:  %s\n", s.Value)
				}
			}
		}
	}
	app.Exit(0)
	return nil
}

// Flags and or commands supplied to cli.
type Flags struct {
	Version VersionFlag `name:"version" help:"Print version information and quit"`

	Server  ServerCmd  `cmd:"" aliases:"s" default:"1" help:"Run the server"`
	Purge   PurgeCmd   `cmd:"" aliases:"p" help:"Purge cache now"`
	Service ServiceCmd `cmd:"" help:"Manage the purge server system service."`
}

// kongVars are the values the command tags interpolate. They live in one place
// so that a parser built anywhere describes the same command line.
func kongVars() kong.Vars {
	return kong.Vars{
		"serviceActions": strings.Join(ServiceAction, ","),
		"defaultSocket":  defaultSocketPath,
		"defaultMode":    defaultSocketMode,
	}
}

// Parse the supplied flags and commands.
func (a *App) ParseFlags() *kong.Context {
	a.flags = &Flags{}

	ctx := kong.Parse(a.flags,
		kong.Name(Name),
		kong.Description(Description),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
		kongVars(),
	)
	return ctx
}
