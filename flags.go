package main

import (
	"fmt"

	"github.com/alecthomas/kong"
)

// When version is requested, print the version.
type VersionFlag bool

func (v VersionFlag) Decode(ctx *kong.DecodeContext) error { return nil }
func (v VersionFlag) IsBool() bool                         { return true }
func (v VersionFlag) BeforeApply(app *kong.Kong, vars kong.Vars) error {
	fmt.Println(serviceName + ": " + serviceVersion)
	app.Exit(0)
	return nil
}

// Flags and or commands supplied to cli.
type Flags struct {
	Version VersionFlag `name:"version" help:"Print version information and quit"`

	Server ServerCmd `cmd:"" aliases:"s" default:"1" help:"Run the server"`
	Purge  PurgeCmd  `cmd:"" aliases:"p" help:"Purge cache now"`
}

// Parse the supplied flags and commands.
func (a *App) ParseFlags() *kong.Context {
	app.flags = &Flags{}

	ctx := kong.Parse(app.flags,
		kong.Name(serviceName),
		kong.Description(serviceDescription),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
	)
	return ctx
}
