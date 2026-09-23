// The hello fixture is deliberately framework-neutral. It is an ordinary
// executable that systemd can run without Provision or a language runtime.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	address := os.Getenv("PROVISION_HTTP_LISTEN")
	if address == "" {
		address = "127.0.0.1:18081"
	}
	revision := os.Getenv("PROVISION_REVISION")
	if revision == "" {
		revision = "hello-v1"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /verify", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"revision": revision})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello from " + revision + "\n"))
	})
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("hello fixture listening on %s as %s", address, strings.TrimSpace(revision))
	log.Fatal(server.ListenAndServe())
}
