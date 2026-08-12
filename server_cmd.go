package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/kardianos/service"
)

// Where the socket goes when the command line does not say. Matches the
// RuntimeDirectory the packaged systemd unit creates.
const defaultSocketPath = "/run/nginx-cache-purge/http.sock"

// What the socket's permissions are set to when the command line does not say.
// Connecting to a UNIX socket requires write permission on it, so this decides
// whether nginx can purge at all. Left to the umask a service manager starts us
// with, the socket comes out 0755, which no other user can connect to however
// the deployment is arranged.
const defaultSocketMode = "0660"

// stopChan carries a shutdown request from the service manager. It is
// buffered so a stop delivered after the signal loop exits cannot block the
// service supervisor.
var stopChan = make(chan struct{}, 1)

// The server command for the CLI to run the HTTP server.
type ServerCmd struct {
	Socket     string   `help:"Socket path for HTTP communication (default ${defaultSocket})." type:"path"`
	SocketMode string   `help:"Octal permissions to give the socket." default:"${defaultMode}"`
	CachePaths []string `name:"cache-path" help:"Cache directory that may be purged, can be repeated. Any path is purgeable when none is given."`
}

// allows reports whether a request may purge the given cache path. The path
// arrives from the caller, and the purge deletes what it finds there, so a
// server started with a list of cache directories will serve no other. Naming
// none keeps every path purgeable, which is what a server without the flag has
// always done.
func (a *ServerCmd) allows(cachePath string) bool {
	if len(a.CachePaths) == 0 {
		return true
	}
	target := resolvePath(cachePath)
	for _, allowed := range a.CachePaths {
		root := resolvePath(allowed)
		// Compare by path element rather than by string prefix, so that
		// /var/cache-other is not taken for a directory inside /var/cache.
		relative, err := filepath.Rel(root, target)
		if err != nil {
			continue
		}
		if relative == "." {
			return true
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvePath canonicalises a path so two spellings of one directory compare
// equal, following symlinks so that a link to an allowed cache is recognised as
// that cache. A path that cannot be resolved is only cleaned: it is one the
// purge is about to fail on anyway, and inventing a resolution for it could let
// it match an allowed directory it does not name.
func resolvePath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// socketMode is the permissions to give the socket, falling back to the default
// when the command line does not say.
func (a *ServerCmd) socketMode() (os.FileMode, error) {
	mode := a.SocketMode
	if mode == "" {
		mode = defaultSocketMode
	}
	parsed, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || parsed > 0o777 {
		return 0, fmt.Errorf("invalid socket mode %q, expected octal permissions such as %s", mode, defaultSocketMode)
	}
	return os.FileMode(parsed), nil
}

// rawQuery splits a query string into its parameters without percent-decoding
// them, which url.Values.Get would do.
func rawQuery(query string) url.Values {
	values := make(url.Values)
	for query != "" {
		var parameter string
		parameter, query, _ = strings.Cut(query, "&")
		if parameter == "" {
			continue
		}
		name, value, _ := strings.Cut(parameter, "=")
		values[name] = append(values[name], value)
	}
	return values
}

// firstValue returns the first value given for a parameter, and whether it was
// present at all. url.Values.Get cannot tell a parameter that was left empty
// from one that was never sent.
func firstValue(values url.Values, name string) (string, bool) {
	given, ok := values[name]
	if !ok || len(given) == 0 {
		return "", false
	}
	return given[0], true
}

// exactRequested reports whether the request asked for the key to be read as a
// literal. Like the other parameters it is taken first-wins, so a client whose
// request URI carries its own exact= cannot override the one the Nginx rewrite
// set ahead of the key. A value that is not a boolean is refused rather than
// assumed false: silently falling back to pattern matching is how a key holding
// ? or [ stops being purged while the response still reports that it was.
func exactRequested(raw, query url.Values) (bool, error) {
	value, ok := firstValue(raw, "exact")
	if !ok {
		value, ok = firstValue(query, "exact")
	}
	if !ok {
		return false, nil
	}
	// A bare exact, with no value at all, asks for it.
	if value == "" {
		return true, nil
	}
	exact, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid exact parameter %q, expected a boolean such as 1", value)
	}
	return exact, nil
}

// Handle request.
func (a *ServerCmd) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Parse query parameters. The key is read twice: nginx substitutes
	// $request_uri into the query string as-is, so the cache key it stored is
	// the escapes and plus signs exactly as they arrive here, while a caller
	// purging by hand is more likely to have escaped the key properly. Decoding
	// is therefore a guess either way, so the purge tries both.
	query := req.URL.Query()
	raw := rawQuery(req.URL.RawQuery)

	// The cache path names a directory rather than a cache key, so the decoded
	// form is the one that matches what is on disk. A value the parser could
	// not decode is dropped rather than reported, so fall back to what was sent
	// instead of answering that no path was given.
	cachePath := query.Get("path")
	if cachePath == "" {
		cachePath = raw.Get("path")
	}
	if cachePath == "" {
		// http.Error must send the message itself. Writing the body first
		// commits a 200 status, leaving the failure indistinguishable from
		// a successful purge.
		http.Error(w, "Need path parameter.", http.StatusBadRequest)
		return
	}
	if !a.allows(cachePath) {
		log.Println("Refusing to purge", cachePath, "as it is not an allowed cache path.")
		http.Error(w, "Cache path is not allowed.", http.StatusForbidden)
		return
	}
	key := raw.Get("key")
	if key == "" {
		http.Error(w, "Need key parameter.", http.StatusBadRequest)
		return
	}
	exact, err := exactRequested(raw, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// An exclude asks for a key to be kept, so both readings of one are
	// honoured rather than picking a side and purging what it named.
	excludes := make([]string, 0, len(raw["exclude"])+len(query["exclude"]))
	excludes = append(excludes, raw["exclude"]...)
	for _, exclude := range query["exclude"] {
		if !slices.Contains(excludes, exclude) {
			excludes = append(excludes, exclude)
		}
	}

	// Purge cache.
	purge := PurgeRequest{
		CachePath:   cachePath,
		Key:         key,
		ExcludeKeys: excludes,
		Exact:       exact,
	}
	purged, err := app.PurgeCache(purge)
	// Nothing matched the key as it was sent, so try it decoded before giving
	// up: that is the same key for all but the callers that escaped it.
	if err == nil && purged == 0 {
		if decoded := query.Get("key"); decoded != "" && decoded != key {
			purge.Key = decoded
			var decodedPurged int
			decodedPurged, err = app.PurgeCache(purge)
			if decodedPurged > 0 {
				key = decoded
				purged = decodedPurged
			}
		}
	}
	// If error, return error. The purge is this server's own work, so a
	// failure is ours to report rather than a bad gateway upstream.
	if err != nil {
		log.Println("Error purging cache:", err)
		http.Error(w, "Error occurred while processing purge.", http.StatusInternalServerError)
		return
	}

	// Successful purge.
	log.Printf("Purged %d cache entries matching %s.\n", purged, key)
	w.Write([]byte("PURGED"))
}

// Bind the UNIX socket, replacing a socket left behind by an earlier run.
func (a *ServerCmd) listen(unixSocket string) (net.Listener, error) {
	// Read the mode before binding, so an unusable one is reported without
	// having taken the socket path over first.
	mode, err := a.socketMode()
	if err != nil {
		return nil, err
	}

	// The socket directory may not exist yet, and net.Listen will not create
	// it. Under systemd RuntimeDirectory this is already there.
	if err := os.MkdirAll(filepath.Dir(unixSocket), 0o755); err != nil {
		return nil, fmt.Errorf("unable to create socket directory: %s", err)
	}

	// A socket file from a previous run has to go before we can bind, but
	// removing one that another instance is still serving would silently take
	// over its requests. Connecting tells the two apart: a refused connection
	// means nothing is listening.
	info, err := os.Lstat(unixSocket)
	switch {
	case err == nil && info.Mode()&os.ModeSocket == 0:
		return nil, fmt.Errorf("%s exists and is not a socket", unixSocket)
	case err == nil:
		conn, dialErr := net.DialTimeout("unix", unixSocket, time.Second)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("%s is already in use by another instance", unixSocket)
		}
		if err := os.Remove(unixSocket); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("unable to remove stale socket: %s", err)
		}
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("unable to check socket path: %s", err)
	}

	listener, err := net.Listen("unix", unixSocket)
	if err != nil {
		return nil, err
	}

	// Bind leaves the socket at whatever the umask allows, so set the mode
	// rather than let the environment we were started from decide who can
	// reach the purge.
	if err := os.Chmod(unixSocket, mode); err != nil {
		listener.Close()
		return nil, fmt.Errorf("unable to set socket permissions: %s", err)
	}

	return listener, nil
}

// Start the HTTP server.
func (a *ServerCmd) Run() error {
	// Determine UNIX socket path.
	unixSocket := a.Socket
	if unixSocket == "" {
		unixSocket = defaultSocketPath
	}

	listener, err := a.listen(unixSocket)
	if err != nil {
		return err
	}
	defer listener.Close()

	// Start the HTTP server. Use our own mux rather than the global default
	// one so the handler registration is scoped to this server.
	log.Println("Starting server at", unixSocket)
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.ServeHTTP)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down on signal instead of dying where we stand, so the listener
	// gets closed and the socket file is unlinked for the next run. No write
	// timeout is set on the server: purging a large cache walks every entry,
	// and a deadline would cut the response off mid-purge.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	// Attach to the service manager when not run interactively, so a stop
	// request arrives on stopChan, then report readiness now that the socket
	// is bound and requests can be served.
	if !service.Interactive() {
		svc, err := new(ServiceCmd).service()
		if err != nil {
			return err
		}
		go svc.Run()
	}
	_, _ = daemon.SdNotify(false, daemon.SdNotifyReady)

	select {
	case err := <-serveErr:
		return err
	case <-stop:
	case <-stopChan:
	}

	log.Println("Shutting down server.")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return err
	}
	// Serve always ends with an error; a closed server is the expected one.
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
