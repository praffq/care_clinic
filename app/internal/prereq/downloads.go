package prereq

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func downloadError(err error) error {
	var connection *net.OpError
	var dns *net.DNSError
	var network net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) ||
		errors.As(err, &connection) || errors.As(err, &dns) || errors.As(err, &network) && network.Timeout() {
		// The native bridge sends errors as text; retain a stable marker and the original cause.
		return fmt.Errorf("download connection interrupted: %w", err)
	}
	return err
}

const dockerLinuxStatic = "https://download.docker.com/linux/static/stable/"

type Download struct {
	Name   string
	URL    string
	SHA256 string
}

type DownloadInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Only retrieve headers: the installer body is not requested before consent.
func inspectDownload(d Download) (DownloadInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadHeaderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, d.URL, nil)
	if err != nil {
		return DownloadInfo{}, err
	}
	client := downloadClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return DownloadInfo{}, fmt.Errorf("could not check the download size: %w", downloadError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return DownloadInfo{}, fmt.Errorf("could not check the download size: the server said %s", resp.Status)
	}
	if resp.ContentLength <= 0 {
		return DownloadInfo{}, fmt.Errorf("the server did not provide the size of %s; try again before downloading", d.Name)
	}
	return DownloadInfo{Name: d.Name, Size: resp.ContentLength}, nil
}

func linuxArch(goarch string) (string, bool) {
	switch goarch {
	case "amd64":
		return "x86_64", true
	case "arm64":
		return "aarch64", true
	}
	return "", false
}

func dockerLinuxDownloads(p *release.Pins, goarch string) (Download, error) {
	arch, ok := linuxArch(goarch)
	if !ok {
		return Download{}, fmt.Errorf("CARE does not install Docker on linux/%s - install it yourself from %s",
			goarch, dockerEnginePage)
	}
	engineSum := p.DockerLinuxX8664SHA256
	if arch == "aarch64" {
		engineSum = p.DockerLinuxAarch64SHA256
	}
	engineName := "docker-" + p.DockerLinuxVersion + ".tgz"
	return Download{Name: engineName, URL: dockerLinuxStatic + arch + "/" + engineName, SHA256: engineSum}, nil
}

func linuxAgentDownload(engine release.EngineUpdate, goarch string) (Download, error) {
	if goarch != "arm64" && goarch != "amd64" {
		return Download{}, fmt.Errorf("CARE does not provide a native agent for linux/%s", goarch)
	}
	asset, ok := engine.Assets["native-linux-"+goarch]
	if !ok {
		return Download{}, fmt.Errorf("signed engine %s has no native Linux %s agent", engine.Version, goarch)
	}
	name := "care-engine-agent-linux-" + goarch
	u, err := url.Parse(asset.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || path.Base(u.Path) != name ||
		path.Base(path.Dir(u.Path)) != "care-engine-v"+engine.Version ||
		asset.Size <= 0 || !validPublishedChecksum(asset.SHA256) {
		return Download{}, fmt.Errorf("signed engine %s has an invalid native agent asset", engine.Version)
	}
	return Download{Name: name, URL: asset.URL, SHA256: asset.SHA256}, nil
}

func validPublishedChecksum(sum string) bool {
	if len(sum) != 64 || sum == strings.Repeat("0", 64) {
		return false
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (pr *Provisioner) signedNativeEngine(version string, fetch bool) (release.EngineUpdate, error) {
	if pr.pins == nil || pr.ReleaseDir == "" {
		return release.EngineUpdate{}, errors.New("native agent installation needs release pins and a manifest trust directory")
	}
	client := release.NewClient(pr.ReleaseDir)
	m, _, err := client.ReadVerified()
	if fetch && errors.Is(err, os.ErrNotExist) {
		ctx, cancel := context.WithTimeout(context.Background(), downloadHeaderTimeout)
		defer cancel()
		m, err = client.Fetch(ctx, pr.pins.UpdatesURL)
	}
	if err != nil {
		return release.EngineUpdate{}, fmt.Errorf("native agent requires a published signed engine manifest: %w", err)
	}
	if m.Engine.Version != version || !updates.Compatible(m.Engine.RequiresDesktop, pr.pins.AppVersion) ||
		!updates.Compatible(">= "+pr.pins.CareVMVersion, version) {
		return release.EngineUpdate{}, fmt.Errorf("signed engine %s is not compatible with required engine %s and desktop %s",
			m.Engine.Version, version, pr.pins.AppVersion)
	}
	for _, blocked := range m.Blocked {
		if blocked == "engine" || blocked == "engine:"+version || blocked == version {
			return release.EngineUpdate{}, fmt.Errorf("signed engine %s is blocked", version)
		}
	}
	return m.Engine, nil
}
