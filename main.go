package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gobwas/glob"
)

// The KEY line lives in the cache entry header, within the first few hundred
// bytes. This bounds how far we read looking for it.
const maxHeaderScan = 64 * 1024

// App structure to access global app variables.
type App struct {
	flags *Flags
}

var app *App

// Regex to determine if a key is a glob pattern. Compiled once, as the server
// purges on every request.
var globRegex = regexp.MustCompile(`[\*?\[{]+`)

// PurgeRequest describes one purge. The fields travel together through the CLI
// and the server, so they are grouped rather than passed as a widening list of
// arguments.
type PurgeRequest struct {
	// CachePath is the directory to purge from, the same one given to
	// proxy_cache_path.
	CachePath string
	// Key is the cache key to purge, read as a wildcard pattern unless Exact
	// says otherwise.
	Key string
	// ExcludeKeys name keys to keep, read the same way as Key.
	ExcludeKeys []string
	// Exact turns pattern matching off, for both the key and the excludes.
	// Whether a key is a pattern is otherwise guessed from the punctuation in
	// it, and real cache keys carry that punctuation: a request URI with a
	// query string puts ? in the key, PHP-style array parameters put [ and ],
	// and either one is read as a pattern that was never meant. Exact is how a
	// caller that knows it holds a literal key says so.
	Exact bool
}

// Function to purge nginx cache keys. It reports how many entries were removed,
// which is what tells a purge that cleared the cache from one that matched
// nothing at all.
func (a *App) PurgeCache(req PurgeRequest) (int, error) {
	// Key must be provided.
	if len(req.Key) == 0 {
		return 0, fmt.Errorf("no key provided")
	}

	// Compile the exclude patterns up front. Doing it per key would repeat
	// the work for every file in the cache, and a pattern that fails to
	// compile has to be fatal: ignoring it would purge the very keys the
	// caller asked to keep. An exact purge has no patterns to compile, so the
	// excludes stay literal and one holding glob punctuation keeps its key
	// rather than failing the purge it appeared in.
	var excludeGlobs []glob.Glob
	if !req.Exact {
		for _, exclude := range req.ExcludeKeys {
			if !globRegex.MatchString(exclude) {
				continue
			}
			g, err := glob.Compile(exclude)
			if err != nil {
				return 0, fmt.Errorf("error while compiling exclude glob %q: %s", exclude, err)
			}
			excludeGlobs = append(excludeGlobs, g)
		}
	}

	// Inline function to check if excludes contains a key.
	keyIsExcluded := func(key string) bool {
		for _, g := range excludeGlobs {
			if g.Match(key) {
				return true
			}
		}
		for _, exclude := range req.ExcludeKeys {
			if exclude == key {
				return true
			}
		}
		return false
	}

	// Confirm that the cache path exists.
	if _, err := os.Stat(req.CachePath); err != nil {
		return 0, fmt.Errorf("cache directory error: %s", err)
	}

	// Count of entries actually removed, reported to the caller.
	purged := 0

	// Check if the key is a wildcard. If its not, we should purge the key by
	// hash, which is also the only thing an exact purge does.
	if req.Exact || !globRegex.MatchString(req.Key) {
		// If excluded, skip the key.
		if keyIsExcluded(req.Key) {
			log.Println("Key", req.Key, "is excluded, will not purge.")
			return 0, nil
		}

		// Get the hash of the key.
		hash := md5.Sum([]byte(req.Key))
		keyHash := hex.EncodeToString(hash[:])

		// Find key in cache directory. The walk reads directory entries rather
		// than calling Lstat on each one, as the name is all this branch
		// compares against and a cache holds a great many files.
		err := filepath.WalkDir(req.CachePath, func(filePath string, entry fs.DirEntry, err error) error {
			// Do not tolerate errors, other than an entry going away while
			// we walk. Nginx maintains the cache as we read it, so entries
			// disappearing mid-walk is expected rather than a failure.
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			// We only care to look at files.
			if entry.IsDir() {
				return nil
			}
			// If this file matches our key hash then delete.
			if entry.Name() == keyHash {
				log.Printf("Purging %s as it matches the key %s requested to be purged.\n", filePath, req.Key)
				err := os.Remove(filePath)
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				if err == nil {
					purged++
				}
				// We're done, so lets stop the walk.
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			return purged, fmt.Errorf("error while scanning for file to purge: %s", err)
		}
	} else {
		// This is a wildcard, so we need to find all files that match it and delete them.
		g, err := glob.Compile(req.Key)
		if err != nil {
			return 0, fmt.Errorf("error while compiling glob: %s", err)
		}
		err = filepath.WalkDir(req.CachePath, func(filePath string, entry fs.DirEntry, err error) error {
			// Do not tolerate errors, other than an entry going away while
			// we walk. Nginx maintains the cache as we read it, so entries
			// disappearing mid-walk is expected rather than a failure.
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			// We only care to look at files.
			if entry.IsDir() {
				return nil
			}

			// Read the file to extract the key.
			file, err := os.Open(filePath)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			keyRead := ""
			keyFound := false
			// Scan file for the key. There is exactly one KEY line per cache
			// entry, in the header, so stop at the first one found. Reading on
			// would scan the cached body for a line that cannot exist, which
			// is why the reader is capped at the header size rather than left
			// to run through gigabytes of cached response body.
			scanner := bufio.NewScanner(io.LimitReader(file, maxHeaderScan))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "KEY: ") {
					keyRead = line[5:]
					keyFound = true
					break
				}
			}
			scanErr := scanner.Err()
			file.Close()

			// A line longer than the scan limit means no cache header here,
			// only binary, so this is not an entry we can match against. The
			// temporary files nginx writes alongside the cache look exactly
			// like this. Anything else is a real read error, which we surface
			// rather than silently leaving a matching key in the cache.
			if scanErr != nil && !errors.Is(scanErr, bufio.ErrTooLong) && !os.IsNotExist(scanErr) {
				return fmt.Errorf("error while reading %s: %s", filePath, scanErr)
			}

			// Without a key, there is nothing to match against.
			if !keyFound {
				return nil
			}

			// If the key matches our glob pattern, delete it.
			if g.Match(keyRead) {
				// If excluded, skip the key.
				if keyIsExcluded(keyRead) {
					log.Println("Key", keyRead, "is excluded, will not purge.")
					return nil
				}

				// Delete the file. An entry nginx already evicted between the
				// walk and here is one less file to purge, not a failure.
				log.Printf("Purging %s with key %s as it matches %s requested to be purged.\n", filePath, keyRead, req.Key)
				err := os.Remove(filePath)
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				if err == nil {
					purged++
				}
			}

			return nil
		})
		if err != nil {
			return purged, fmt.Errorf("error while scanning for file to purge: %s", err)
		}
	}
	return purged, nil
}

// Main function to start the app.
func main() {
	app = new(App)
	ctx := app.ParseFlags()

	// Run the command requested.
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}
