package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"time"
)

// carePingURL is CARE's health endpoint through the service's 443 forward;
// the Caddyfile answers localhost with its internal CA, so TLS isn't checked.
const carePingURL = "https://localhost/ping/"

// ServiceHealth is one container of the clinic stack.
type ServiceHealth struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// CareHealth is the Health card: whether CARE answers its ping and the state
// of each container in the stack.
type CareHealth struct {
	Healthy  bool            `json:"healthy"`
	Code     int             `json:"code"`
	Detail   string          `json:"detail"`
	Services []ServiceHealth `json:"services"`
	// ServicesError is set when the engine couldn't list the containers.
	ServicesError string    `json:"servicesError,omitempty"`
	CheckedAt     time.Time `json:"checkedAt"`
}

// CheckCareHealth pings CARE and lists the stack's containers.
func (a *App) CheckCareHealth() CareHealth {
	h := pingCare(a.clientContext(), a.pingURL())
	out, err := a.engine().Status()
	if err != nil {
		h.ServicesError = err.Error()
	}
	for _, line := range strings.Split(out, "\n") {
		if name, state, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			h.Services = append(h.Services, ServiceHealth{Name: name, State: state})
		}
	}
	h.CheckedAt = time.Now()
	return h
}

func (a *App) pingURL() string {
	if a.carePingURL != "" {
		return a.carePingURL
	}
	return carePingURL
}

func pingCare(ctx context.Context, url string) CareHealth {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{
		// #nosec G402 -- local health probe of CARE's own internal-CA endpoint.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return CareHealth{Detail: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return CareHealth{Detail: "CARE isn't answering on this computer"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return CareHealth{Healthy: true, Code: resp.StatusCode, Detail: "CARE is answering"}
	}
	return CareHealth{Code: resp.StatusCode, Detail: "CARE answered HTTP " + strings.TrimSpace(resp.Status)}
}
