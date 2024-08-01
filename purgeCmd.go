package main

// Purge command for CLI to purge cache keys.
type PurgeCmd struct {
	CachePath   string   `arg:"" name:"cache-path" help:"Path to cache directory." type:"existingdir"`
	Key         string   `arg:"" name:"key" help:"Cache key or wildcard match."`
	ExcludeKeys []string `optional:"" name:"exclude-key" help:"Key to exclude, can be wild card and can add multiple excludes."`
}

// The purge command execution just runs the apps purge cache function.
func (a *PurgeCmd) Run() error {
	return app.PurgeCache(a.CachePath, a.Key, a.ExcludeKeys)
}
