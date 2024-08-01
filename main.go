package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gobwas/glob"
)

// Basic application info.
const (
	serviceName        = "nginx-cache-purge"
	serviceDescription = "Tool to help purge Nginx cache "
	serviceVersion     = "0.1"
)

// App structure to access global app variables.
type App struct {
	flags *Flags
}

var app *App

// Function to purge nginx cache keys.
func (a *App) PurgeCache(CachePath string, Key string, ExcludeKeys []string) error {
	// Key must be provided.
	if len(Key) == 0 {
		return fmt.Errorf("no key provided")
	}

	// Regex to determine if key is a glob pattern.
	globRegex := regexp.MustCompile(`[\*?\[{]+`)

	// Inline function to check if excludes contains a key.
	keyIsExcluded := func(Key string) bool {
		for _, exclude := range ExcludeKeys {
			if globRegex.MatchString(exclude) {
				g, err := glob.Compile(exclude)
				if err != nil && g != nil && g.Match(Key) {
					return true
				}
			}
			if exclude == Key {
				return true
			}
		}
		return false
	}

	// Confirm that the cache path exists.
	if _, err := os.Stat(CachePath); err != nil {
		return fmt.Errorf("cache directory error: %s", err)
	}

	// Check if the key is a wildcard. If its not, we should purge the key by hash.
	if !globRegex.MatchString(Key) {
		// If excluded, skip the key.
		if keyIsExcluded(Key) {
			fmt.Println("Key", Key, "is excluded, will not purge.")
			return nil
		}

		// Get the hash of the key.
		hash := md5.Sum([]byte(Key))
		keyHash := hex.EncodeToString(hash[:])

		// Find key in cache directory.
		err := filepath.Walk(CachePath, func(filePath string, info os.FileInfo, err error) error {
			// Do not tolerate errors.
			if err != nil {
				return err
			}
			// We only care to look at files.
			if info.IsDir() {
				return nil
			}
			// If this file matches our key hash then delete.
			if info.Name() == keyHash {
				fmt.Printf("Purging %s as it matches the key %s requested to be purged.\n", filePath, Key)
				err := os.Remove(filePath)
				if err != nil {
					return err
				}
				// We're done, so lets stop the walk.
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error while scanning for file to purge: %s", err)
		}
	} else {
		// This is a wildcard, so we need to find all files that match it and delete them.
		g, err := glob.Compile(Key)
		if err != nil {
			return fmt.Errorf("error while compiling glob: %s", err)
		}
		err = filepath.Walk(CachePath, func(filePath string, info os.FileInfo, err error) error {
			// Do not tolerate errors.
			if err != nil {
				return err
			}
			// We only care to look at files.
			if info.IsDir() {
				return nil
			}

			// Read the file to extract the key.
			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "KEY: ") {
					key := line[5:]
					if g.Match(key) {
						fmt.Printf("Purging %s as it matches the key %s requested to be purged.\n", filePath, Key)
						err := os.Remove(filePath)
						if err != nil {
							return err
						}
						break
					}
				}
			}

			return nil
		})
		if err != nil {
			return fmt.Errorf("error while scanning for file to purge: %s", err)
		}
	}
	return nil
}

// Main function to start the app.
func main() {
	app = new(App)
	ctx := app.ParseFlags()

	// Run the command requested.
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}
