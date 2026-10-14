package prereq

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

const (
	dockerLinuxBinDir    = "/usr/local/bin"
	dockerLinuxUnitPath  = "/etc/systemd/system/docker.service"
	careAgentBinPath     = "/usr/local/bin/care-engine-agent"
	careAgentUnitPath    = "/etc/systemd/system/care-engine-agent.service"
	careAgentVersionPath = "/usr/local/share/care-clinic/agent.version"
	careStateDir         = "/var/lib/care"
	careClinicDir        = "/var/lib/care/clinic"
	careBackupDir        = "/var/lib/care/backups"
	careAgentSocket      = "/run/care-engine-agent/control.sock"
	careAgentReadyWait   = 30 * time.Second
)

const dockerLinuxUnit = `[Unit]
Description=Docker Application Container Engine (installed by CARE Clinic)
Documentation=https://docs.docker.com
After=network-online.target firewalld.service time-set.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=` + dockerLinuxBinDir + `/dockerd
ExecReload=/bin/kill -s HUP $MAINPID
TimeoutStartSec=0
Restart=always
RestartSec=2
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Delegate=yes
KillMode=process
OOMScoreAdjust=-500

[Install]
WantedBy=multi-user.target
`

const careAgentLinuxUnit = `[Unit]
Description=CARE Clinic local agent
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
User=root
Group=__CARE_AGENT_GROUP__
UMask=0007
RuntimeDirectory=care-engine-agent
RuntimeDirectoryMode=0750
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=` + careAgentBinPath + ` --socket ` + careAgentSocket + `
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`

func linuxInstallCommand(pm, pkg string) string {
	switch pm {
	case "apt":
		return "apt-get update && apt-get install -y " + pkg
	case "dnf":
		return "dnf install -y " + pkg
	case "zypper":
		return "zypper --non-interactive install " + pkg
	case "pacman":
		return "pacman -Sy --noconfirm " + pkg
	}
	return ""
}

func linuxDockerPluginPackages(pm string) []string {
	switch pm {
	case "apt":
		return []string{"docker-compose-v2", "docker-buildx"}
	case "dnf":
		return []string{"docker-compose-plugin", "docker-buildx-plugin"}
	case "zypper":
		return []string{"docker-compose", "docker-buildx"}
	case "pacman":
		return []string{"docker-compose", "docker-buildx"}
	default:
		return nil
	}
}

func (pr *Provisioner) installDockerLinux() error {
	// Inspect provisioning prerequisites only; daemon readiness is checked
	// through the agent after both system services have been installed.
	if hasCommand("dockerd") {
		if err := elevate.Run("systemctl enable --now docker", true); err != nil {
			return fmt.Errorf("could not start the native Docker service: %w", err)
		}
		return pr.installAgentLinux()
	}
	engine, err := dockerLinuxDownloads(pr.pins, runtime.GOARCH)
	if err != nil {
		return err
	}
	if !hasCommand("systemctl") {
		return fmt.Errorf("this system does not use systemd, so CARE cannot run Docker as a service - install Docker from %s",
			dockerEnginePage)
	}
	var steps []string
	if !hasCommand("iptables") {
		pm := linuxPackageManager()
		if pm == "" {
			return errors.New("Docker needs iptables, which is not installed, and no supported package manager " +
				"was found - install iptables, then try again")
		}
		steps = append(steps, linuxInstallCommand(pm, "iptables"))
	}

	dir, err := os.MkdirTemp("", "care-docker-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	unit := filepath.Join(dir, "docker.service")
	if err := os.WriteFile(unit, []byte(dockerLinuxUnit), 0o644); err != nil {
		return err
	}
	archive, err := pr.download(engine)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()
	extracted := filepath.Join(dir, "engine")
	steps = append(steps,
		"mkdir "+elevate.ShQuote(extracted),
		"tar -xzf "+elevate.ShQuote(archive)+" -C "+elevate.ShQuote(extracted),
		"install -m 0755 "+elevate.ShQuote(extracted)+"/docker/* "+dockerLinuxBinDir+"/",
		"install -m 0644 "+elevate.ShQuote(unit)+" "+dockerLinuxUnitPath,
		"groupadd -f docker",
		"usermod -aG docker "+elevate.ShQuote(currentUsername()),
		"systemctl daemon-reload",
		"systemctl enable --now docker",
	)
	sh := "trap " + elevate.ShQuote("rm -rf "+elevate.ShQuote(extracted)) + " EXIT; " + strings.Join(steps, " && ")
	pr.logln("Installing Docker Engine " + pr.pins.DockerLinuxVersion + ". You'll be asked for your password...")
	if err := elevate.Run(sh, true); err != nil {
		return fmt.Errorf("could not install Docker: %w", err)
	}
	pr.logln("Docker installed. Installing the CARE agent before checking container readiness.")
	return pr.installAgentLinux()
}

func (pr *Provisioner) installAgentLinux() error {
	if runtime.GOOS != "linux" {
		return errors.New("the native CARE agent is only installed on Linux")
	}
	if !hasCommand("systemctl") {
		return errors.New("CARE needs systemd to run the native agent as a service")
	}
	if pr.pins == nil {
		return errors.New("this build of CARE Clinic has no download pins")
	}
	if _, err := os.Lstat(nativeUpdateMarker); err == nil {
		return errors.New("an interrupted CARE agent update needs recovery through the update coordinator before repair")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect native agent update: %w", err)
	}
	if err := pr.ensureNativeDataDirs(); err != nil {
		return err
	}
	installed, installedErr := os.ReadFile(careAgentVersionPath)
	_, binaryErr := os.Stat(careAgentBinPath)
	switch {
	case binaryErr == nil:
		if installedErr != nil {
			return errors.New("the installed CARE agent has no recorded version; update it through CARE's update coordinator")
		}
		if version := strings.TrimSpace(string(installed)); version != pr.pins.CareVMVersion {
			return fmt.Errorf("the installed CARE agent is %s; version %s must be applied through CARE's update coordinator",
				version, pr.pins.CareVMVersion)
		}
		if err := pr.enableNativeAgentService(""); err != nil {
			return err
		}
		if err := waitForDockerReadyOrLoginRefresh(careAgentReadyWait, func() Status {
			return DockerCheck(proc.Runner{})
		}, pr.logln); err != nil {
			return err
		}
		return pr.ensureDockerPluginsLinux()
	case !errors.Is(binaryErr, os.ErrNotExist):
		return fmt.Errorf("could not inspect the installed CARE agent: %w", binaryErr)
	case installedErr == nil:
		return errors.New("the CARE agent version record exists but its binary is missing; repair it through CARE's update coordinator")
	case !errors.Is(installedErr, os.ErrNotExist):
		return fmt.Errorf("could not read the installed CARE agent version: %w", installedErr)
	}
	engine, err := pr.signedNativeEngine(pr.pins.CareVMVersion, true)
	if err != nil {
		return err
	}
	download, err := linuxAgentDownload(engine, runtime.GOARCH)
	if err != nil {
		return err
	}
	agent, err := pr.download(download)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(agent) }()
	group, err := currentPrimaryGroup()
	if err != nil {
		return err
	}
	dir := filepath.Dir(agent)
	unit := filepath.Join(dir, "care-engine-agent.service")
	if err := os.WriteFile(unit, []byte(careAgentServiceUnit(group)), 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(unit) }()
	versionFile := filepath.Join(dir, "agent.version")
	if err := os.WriteFile(versionFile, []byte(pr.pins.CareVMVersion+"\n"), 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(versionFile) }()
	pr.logln("Installing the CARE agent " + pr.pins.CareVMVersion + ". You'll be asked for your password...")
	command := strings.Join([]string{
		"install -D -m 0755 " + elevate.ShQuote(agent) + " " + careAgentBinPath,
		"install -m 0644 " + elevate.ShQuote(unit) + " " + careAgentUnitPath,
		"install -D -m 0644 " + elevate.ShQuote(versionFile) + " " + careAgentVersionPath,
		"systemctl daemon-reload",
		"systemctl enable --now care-engine-agent",
	}, " && ")
	if err := elevate.Run(command, true); err != nil {
		return fmt.Errorf("could not install the CARE agent: %w", err)
	}
	pr.logln("CARE agent installed with access for this desktop account's primary group.")
	if err := waitForDockerReadyOrLoginRefresh(careAgentReadyWait, func() Status {
		return DockerCheck(proc.Runner{})
	}, pr.logln); err != nil {
		return err
	}
	return pr.ensureDockerPluginsLinux()
}

func (pr *Provisioner) enableNativeAgentService(group string) error {
	if group != "" {
		unit := careAgentServiceUnit(group)
		dir, err := os.MkdirTemp("", "care-agent-unit-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "care-engine-agent.service")
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			return err
		}
		command := "install -m 0644 " + elevate.ShQuote(path) + " " + careAgentUnitPath + " && systemctl daemon-reload && systemctl enable --now care-engine-agent"
		if err := elevate.Run(command, true); err != nil {
			return fmt.Errorf("could not configure the CARE agent service: %w", err)
		}
		return nil
	}
	return elevate.Run("systemctl enable --now care-engine-agent", true)
}

// NativeAgentVersions reports the installed native agent version and this
// build's required version. Agent version changes belong to the update
// coordinator; prerequisite repair only installs a missing initial agent.
func (pr *Provisioner) NativeAgentVersions() (installed, expected string, err error) {
	if runtime.GOOS != "linux" {
		return "", "", nil
	}
	if pr.pins != nil {
		expected = pr.pins.CareVMVersion
	}
	data, err := os.ReadFile(careAgentVersionPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, binaryErr := os.Stat(careAgentBinPath); binaryErr == nil {
			return "", expected, errors.New("the installed CARE agent has no recorded version")
		} else if !errors.Is(binaryErr, os.ErrNotExist) {
			return "", expected, binaryErr
		}
		return "", expected, nil
	}
	if err != nil {
		return "", expected, err
	}
	if installed = strings.TrimSpace(string(data)); installed == "" {
		return "", expected, errors.New("the installed CARE agent has an empty version record")
	}
	if info, err := os.Stat(careAgentBinPath); err != nil || !info.Mode().IsRegular() {
		return installed, expected, errors.New("the CARE agent version record exists but its binary is missing or invalid")
	}
	return installed, expected, nil
}

func (pr *Provisioner) ensureNativeDataDirs() error {
	if err := elevate.Run(nativeDataDirSetupCommand(), true); err != nil {
		return fmt.Errorf("could not prepare private CARE agent directories: %w", err)
	}
	return nil
}

func nativeDataDirSetupCommand() string {
	return strings.Join([]string{
		"install -d -o root -g root -m 0700 " + careStateDir,
		"install -d -o root -g root -m 0700 " + careClinicDir,
		"install -d -o root -g root -m 0755 " + careBackupDir,
	}, " && ")
}

func careAgentServiceUnit(group string) string {
	return strings.Replace(careAgentLinuxUnit, "Group=__CARE_AGENT_GROUP__", "Group="+group, 1)
}

func currentPrimaryGroup() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("could not determine the desktop account's primary group: %w", err)
	}
	if _, err := strconv.ParseUint(u.Gid, 10, 32); err != nil {
		return "", fmt.Errorf("the desktop account has an invalid primary group ID %q", u.Gid)
	}
	return u.Gid, nil
}

func (pr *Provisioner) ensureDockerPluginsLinux() error {
	run := proc.Runner{}
	_, composeErr := run.AgentCapture("compose", "version")
	_, buildxErr := run.AgentCapture("buildx", "version")
	if composeErr == nil && buildxErr == nil {
		return nil
	}
	pm := linuxPackageManager()
	packages := linuxDockerPluginPackages(pm)
	if len(packages) == 0 {
		return errors.New("CARE needs the Docker Compose and Buildx plugins; install them with this system's package manager")
	}
	pr.logln("Installing Docker Compose and Buildx from the system package manager...")
	if err := elevate.Run(linuxInstallCommand(pm, strings.Join(packages, " ")), true); err != nil {
		return fmt.Errorf("could not install the Docker Compose and Buildx plugins: %w", err)
	}
	if _, err := run.AgentCapture("compose", "version"); err != nil {
		return fmt.Errorf("Docker Compose did not become available: %w", err)
	}
	if _, err := run.AgentCapture("buildx", "version"); err != nil {
		return fmt.Errorf("Docker Buildx did not become available: %w", err)
	}
	return nil
}
