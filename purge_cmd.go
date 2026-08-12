package main

import "log"

// Purge command for CLI to purge cache keys.
type PurgeCmd struct {
	CachePath   string   `arg:"" name:"cache-path" help:"Path to cache directory." type:"existingdir"`
	Key         string   `arg:"" name:"key" help:"Cache key or wildcard match."`
	ExcludeKeys []string `optional:"" name:"exclude-key" help:"Key to exclude, can be wild card and can add multiple excludes."`
	Exact       bool     `optional:"" name:"exact" help:"Treat the key and excludes as literal keys rather than wildcard matches."`
}

// The purge command execution just runs the apps purge cache function, then
// says how much it removed. A key that matched nothing is not an error, so the
// count is the only thing that distinguishes it from a purge that worked.
func (a *PurgeCmd) Run() error {
	purged, err := app.PurgeCache(PurgeRequest{
		CachePath:   a.CachePath,
		Key:         a.Key,
		ExcludeKeys: a.ExcludeKeys,
		Exact:       a.Exact,
	})
	if err != nil {
		return err
	}
	log.Printf("Purged %d cache entries matching %s.\n", purged, a.Key)
	return nil
}
