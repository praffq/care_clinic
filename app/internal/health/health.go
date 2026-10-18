package health

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Health struct {
	Active bool   `json:"active"`
	Code   int    `json:"code"`
	Detail string `json:"detail"`
}

func Ping() Health {
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Get("https://localhost/ping/")
	if err != nil {
		return Health{Active: false, Code: 0, Detail: "nothing answering on :443"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 200 {
		return Health{Active: true, Code: 200, Detail: "healthy"}
	}
	return Health{Active: false, Code: resp.StatusCode, Detail: "HTTP " + strings.TrimSpace(resp.Status)}
}

func Wait(log func(string), timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := Health{Detail: "nothing answering yet"}
	for n := 1; ; n++ {
		if last = Ping(); last.Active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("CARE did not become healthy within %s (%s)", timeout, last.Detail)
		}
		logln(log, fmt.Sprintf("  waiting for CARE to answer... (%d) - %s", n, last.Detail))
		time.Sleep(3 * time.Second)
	}
}

func logln(log func(string), s string) {
	if log != nil {
		log(s)
	}
}
