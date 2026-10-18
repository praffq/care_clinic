// Command care-engine-agent is a placeholder agent baked into the engine VM
// image. It only reports engine status; the real agent replaces it later.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"time"
)

var version = "dev"

type status struct {
	EngineVersion string `json:"engine_version"`
	DockerReady   bool   `json:"docker_ready"`
}

func dockerReady(socket string) bool {
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
	resp, err := client.Get("http://docker/_ping")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func main() {
	addr := flag.String("listen", ":47377", "TCP listen address")
	socket := flag.String("docker-socket", "/var/run/docker.sock", "Docker daemon socket")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		s := status{EngineVersion: version, DockerReady: dockerReady(*socket)}
		log.Printf("%s %s docker_ready=%t", r.Method, r.URL.Path, s.DockerReady)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})

	log.Printf("care-engine-agent %s listening on %s", version, *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
