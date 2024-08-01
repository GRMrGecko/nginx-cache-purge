package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
)

// The server command for the CLI to run the HTTP server.
type ServerCmd struct {
	Socket string `help:"Socket path for HTTP communication." type:"path"`
}

// Handle request.
func (a *ServerCmd) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Parse query parameters.
	query := req.URL.Query()
	cachePath := query.Get("path")
	if cachePath == "" {
		io.WriteString(w, "Need path parameter.")
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	key := query.Get("key")
	if key == "" {
		io.WriteString(w, "Need key parameter.")
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	excludes := query["exclude"]

	// Purge cache.
	err := app.PurgeCache(cachePath, key, excludes)
	// If error, return error.
	if err != nil {
		fmt.Println("Error purging cache:", err)
		io.WriteString(w, "Error occurred while processing purge.")
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}

	// Successful purge.
	w.Write([]byte("PURGED"))
}

// Start the FastCGI server.
func (a *ServerCmd) Run() error {
	// Determine UNIX socket path.
	unixSocket := a.Socket
	if unixSocket == "" {
		unixSocket = "/var/run/nginx-cache-purge/http.sock"
	}

	// If socket exists, remove it.
	if _, err := os.Stat(unixSocket); !os.IsNotExist(err) {
		os.Remove(unixSocket)
	}

	// Open the socket for FCGI communication.
	listener, err := net.Listen("unix", unixSocket)
	if err != nil {
		return err
	}
	defer listener.Close()

	// Start the FastCGI server.
	log.Println("Starting server at", unixSocket)
	http.HandleFunc("/", a.ServeHTTP)
	err = http.Serve(listener, nil)

	return err
}
