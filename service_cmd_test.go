package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"
)

// The allowlist has to reach the installed unit's ExecStart, as that is the
// only place the server it starts reads it from.
func TestServiceArguments(t *testing.T) {
	tests := []struct {
		name       string
		cachePaths []string
		want       []string
	}{
		{
			// What the command has always installed, so a service installed
			// without the flag runs exactly as it did before.
			name: "no allowlist",
			want: []string{"server"},
		},
		{
			name:       "one cache path",
			cachePaths: []string{"/var/nginx/proxy_temp/cache"},
			want:       []string{"server", "--cache-path", "/var/nginx/proxy_temp/cache"},
		},
		{
			// The flag repeats, and each one has to arrive as its own argument
			// rather than joined into a value the server reads as one path.
			name:       "several cache paths",
			cachePaths: []string{"/var/cache/one", "/var/cache/two"},
			want: []string{
				"server",
				"--cache-path", "/var/cache/one",
				"--cache-path", "/var/cache/two",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := &ServiceCmd{Action: "install", CachePaths: test.cachePaths}
			require.Equal(t, test.want, cmd.arguments())

			// The same arguments have to be what the service definition is
			// built with, or the unit is written without them.
			svc, err := cmd.service()
			require.NoError(t, err)
			require.NotNil(t, svc)
		})
	}
}

// The unit runs from a working directory of the service manager's choosing, so
// a relative path would name a different directory there than the one the
// install was typed against.
func TestServiceArgumentsAbsolutePaths(t *testing.T) {
	cmd := &ServiceCmd{Action: "install", CachePaths: []string{"cache"}}

	arguments := cmd.arguments()

	require.Len(t, arguments, 3)
	require.True(t, filepath.IsAbs(arguments[2]), "%s is not absolute", arguments[2])
	require.Equal(t, "cache", filepath.Base(arguments[2]))
}

// A symlinked cache is resolved per request by the server, so the install must
// leave the link alone rather than pin the allowlist to today's target.
func TestServiceArgumentsKeepsSymlinks(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "cache")
	link := filepath.Join(base, "link")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.Symlink(target, link))

	cmd := &ServiceCmd{Action: "install", CachePaths: []string{link}}

	require.Equal(t, []string{"server", "--cache-path", link}, cmd.arguments())
}

// The allowlist is written into the unit at install, so accepting it on an
// action that cannot act on it would read as having changed the allowlist of a
// service that carries on with the one it was installed with.
func TestServiceRejectsCachePathsOutsideInstall(t *testing.T) {
	for _, action := range ServiceAction {
		if action == "install" {
			continue
		}
		t.Run(action, func(t *testing.T) {
			cmd := &ServiceCmd{Action: action, CachePaths: []string{t.TempDir()}}

			err := cmd.Run()

			require.Error(t, err)
			require.Contains(t, err.Error(), "--cache-path")
		})
	}
}

// The flag has to be reachable from the command line it is documented on, and
// kong's path type has to expand each value rather than only the first.
func TestServiceCachePathFlagParses(t *testing.T) {
	flags := &Flags{}
	parser, err := kong.New(flags, kong.Name(Name), kongVars())
	require.NoError(t, err)

	_, err = parser.Parse([]string{
		"service", "install",
		"--cache-path", "/var/cache/one",
		"--cache-path", "relative/cache",
	})
	require.NoError(t, err)

	require.Equal(t, "install", flags.Service.Action)
	require.Len(t, flags.Service.CachePaths, 2)
	require.Equal(t, "/var/cache/one", flags.Service.CachePaths[0])
	require.True(t, filepath.IsAbs(flags.Service.CachePaths[1]), "relative path was not expanded")
}
