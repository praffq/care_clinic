package clinic

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/envconfig"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
)

var domainFiles = []string{"Caddyfile", "setup/index.html"}
var hostTokenRe = regexp.MustCompile(`[A-Za-z0-9_.-]+`)
var setupHostRe = regexp.MustCompile(`id="clinic-address" href="https://([A-Za-z0-9-]+\.local)"`)
var caddyHostRe = regexp.MustCompile(`(?m)^([A-Za-z0-9-]+\.local):443[ \t]*\{\r?\n[ \t]*tls internal\r?\n[ \t]*import bootstrap\r?\n[ \t]*import site\r?\n\}`)

func (e *Clinic) ApplyDomain() error {
	if err := mdns.ValidateLabel(e.mdnsName()); err != nil {
		return err
	}
	files := map[string][]byte{}
	managedHosts := map[string]bool{"example.local": true}
	for _, rel := range domainFiles {
		b, err := os.ReadFile(filepath.Join(e.InstallDir, filepath.FromSlash(rel)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		files[rel] = b
	}
	for _, pattern := range []*regexp.Regexp{caddyHostRe, setupHostRe} {
		for _, b := range files {
			for _, match := range pattern.FindAllStringSubmatch(string(b), -1) {
				managedHosts[strings.ToLower(match[1])] = true
			}
		}
	}
	if err := envconfig.Generate(e.InstallDir, e.host()); err != nil {
		return err
	}
	for rel, b := range files {
		out := hostTokenRe.ReplaceAllStringFunc(string(b), func(token string) string {
			if managedHosts[strings.ToLower(token)] {
				return e.host()
			}
			return token
		})
		if out == string(b) {
			continue
		}
		path := filepath.Join(e.InstallDir, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := atomicfile.Write(path, []byte(out), info.Mode().Perm()); err != nil {
			return err
		}
	}
	e.logln("Clinic address set to https://" + e.host() + "/")
	return nil
}

func (e *Clinic) host() string { return mdns.Label(e.mdnsName()) + ".local" }
func (e *Clinic) Host() string { return e.host() }
