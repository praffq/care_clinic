package trust

import (
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func installSh(path string) string {
	return installShFor(runtime.GOOS, path)
}

func installShFor(goos, path string) string {
	if goos == "darwin" {
		return "security add-trusted-cert -d -r trustRoot -p ssl -k /Library/Keychains/System.keychain " +
			elevate.ShQuote(path)
	}
	q := elevate.ShQuote(path)
	return "cp " + q + " /usr/local/share/ca-certificates/care-root.crt && update-ca-certificates " +
		"|| { cp " + q + " /etc/pki/ca-trust/source/anchors/care-root.crt && update-ca-trust; }"
}

func installPS(path string) string {
	return "certutil -addstore -f Root " + elevate.PSQuote(path)
}

func installUnprivileged(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = installLoginKeychainCommand(path)
	case "windows":
		cmd = proc.Command("certutil", "-addstore", "-f", "Root", path)
	case "linux":
		cmd = proc.Command("sh", "-c", installSh(path))
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func installLoginKeychainCommand(path string) *exec.Cmd {
	return proc.Command("security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl",
		"-k", os.Getenv("HOME")+"/Library/Keychains/login.keychain-db", path)
}

type Install struct {
	Step elevate.Step
	log  func(string)
	host string
	path string
}

func (i Install) TryWithoutAdmin() bool {
	_ = installUnprivileged(i.path)
	if !HostTrusts(i.host) {
		return false
	}
	logln(i.log, "This machine now trusts https://"+i.host+"/.")
	return true
}

func Step(log func(string), host, rootPEM string) (Install, func(), bool) {
	noop := func() {}
	if HostTrusts(host) {
		return Install{}, noop, false
	}
	if rootPEM == "" {
		logln(log, "Could not prepare local certificate trust: CARE's root certificate is not available yet.")
		return Install{}, noop, false
	}
	f, err := os.CreateTemp("", "care-root-*.crt")
	if err != nil {
		logln(log, "Could not prepare local certificate trust: "+err.Error())
		return Install{}, noop, false
	}
	path := f.Name()
	if _, err := f.WriteString(rootPEM); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		logln(log, "Could not prepare local certificate trust: "+err.Error())
		return Install{}, noop, false
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		logln(log, "Could not prepare local certificate trust: "+err.Error())
		return Install{}, noop, false
	}
	cleanup := func() { _ = os.Remove(path) }
	return Install{
		Step: elevate.Step{
			What: "trust CARE's security certificate, so the browser shows no warning",
			Sh:   installSh(path),
			PS:   installPS(path),
		},
		log:  log,
		host: host,
		path: path,
	}, cleanup, true
}

func HostTrusts(host string) bool {
	tlsConfig := &tls.Config{ServerName: host}
	if runtime.GOOS == "linux" {
		tlsConfig.RootCAs = rootsFromFiles(linuxTrustBundles)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 4 * time.Second}, "tcp", "127.0.0.1:443", tlsConfig)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func HostServed(host string) bool {
	tlsConfig := &tls.Config{ServerName: host, InsecureSkipVerify: true}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 4 * time.Second}, "tcp", "127.0.0.1:443", tlsConfig)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	certs := conn.ConnectionState().PeerCertificates
	return len(certs) > 0 && certs[0].VerifyHostname(host) == nil
}

func rootsFromFiles(paths []string) *x509.CertPool {
	roots := x509.NewCertPool()
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			roots.AppendCertsFromPEM(data)
		}
	}
	return roots
}

// Set by our Caddyfile's pki block, so a cert carrying it is ours by construction.
const CommonName = "CARE Clinic Local CA"

var linuxCAAnchors = []string{
	"/usr/local/share/ca-certificates/care-root.crt",
	"/etc/pki/ca-trust/source/anchors/care-root.crt",
}

var linuxTrustBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/pki/tls/certs/ca-bundle.crt",
}

func Untrust(log func(string), confirm func(string, string) bool, rootPEM string) string {
	fp := SHA1Hex(rootPEM)
	removed, err := removeTrustedRoots(confirm, fp)
	present, inspectErr := Inspect()
	if present || err != nil || inspectErr != nil {
		detail := ""
		if joined := errors.Join(err, inspectErr); joined != nil {
			detail = " (" + joined.Error() + ")"
		}
		logln(log, "Could not remove CARE's certificate from this machine's trust store"+detail+
			". Remove \""+CommonName+"\" by hand if you want it gone.")
		if !present && inspectErr == nil {
			return "Removing the certificate \"" + CommonName + "\" did not finish cleanly" + detail +
				". " + manualRemoval()
		}
		return "The certificate \"" + CommonName + "\" is still trusted by this computer" + detail +
			". " + manualRemoval()
	}
	switch {
	case removed:
		logln(log, "Removed CARE's certificate from this machine's trust store.")
	case fp == "":
		logln(log, "Note: couldn't read CARE's certificate before teardown, and found none to remove. "+
			"If a browser still trusts \""+CommonName+"\", remove it by hand.")
	}
	return ""
}

func manualRemoval() string {
	switch runtime.GOOS {
	case "darwin":
		return "Remove it in Keychain Access, or run: security delete-certificate -c " +
			elevate.ShQuote(CommonName)
	case "windows":
		return "Remove it in certmgr.msc, or run as administrator: certutil -delstore Root " +
			elevate.PSQuote(CommonName)
	}
	return "Delete " + strings.Join(linuxCAAnchors, " and ") + ", then run update-ca-certificates or update-ca-trust."
}

func removeTrustedRoots(confirm func(string, string) bool, fp string) (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return removeTrustedRootsDarwin(confirm, fp)
	case "windows":
		return removeTrustedRootsWindows(fp)
	case "linux":
		return removeTrustedRootsLinux(confirm)
	}
	return false, nil
}

const darwinSystemKeychain = "/Library/Keychains/System.keychain"

func darwinLoginKeychain() string {
	return os.Getenv("HOME") + "/Library/Keychains/login.keychain-db"
}

func removeTrustedRootsDarwin(confirm func(string, string) bool, fp string) (bool, error) {
	login := darwinLoginKeychain()
	removed := false
	var removalErrors []error
	hashes, err := darwinCARoots(login, fp)
	if err != nil {
		return false, err
	}
	for _, h := range hashes {
		// -t deletes user trust settings along with the login certificate.
		// No separate remove-trusted-cert invocation is needed in this domain.
		_, deleteErr := proc.Command("security", "delete-certificate", "-t", "-Z", h, login).CombinedOutput()
		if deleteErr != nil {
			removalErrors = append(removalErrors, fmt.Errorf("could not remove login-keychain certificate %s: %w", h, deleteErr))
			continue
		}
		removed = true
	}

	const sys = darwinSystemKeychain
	hashes, err = darwinCARoots(sys, fp)
	if err != nil {
		return removed, errors.Join(errors.Join(removalErrors...), err)
	}
	if len(hashes) == 0 {
		return removed, errors.Join(removalErrors...)
	}
	if confirm == nil || !confirm("Remove CARE's certificate?",
		"Remove CARE's security certificate from this computer. Approve the system permission prompt to continue.") {
		return removed, errors.Join(removalErrors...)
	}
	cmds := make([]string, 0, len(hashes))
	var cleanups []func()
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	for _, h := range hashes {
		cert, err := darwinCertificatePEM(sys, h)
		if err != nil {
			return removed, errors.Join(errors.Join(removalErrors...), err)
		}
		path, cleanup, err := writeRemovalCertificate(cert)
		if err != nil {
			return removed, errors.Join(errors.Join(removalErrors...), err)
		}
		cleanups = append(cleanups, cleanup)
		cmds = append(cmds, darwinRemoveCertificateCommand(h, path, sys, true))
	}
	if err := elevate.Run(strings.Join(cmds, " && "), true); err != nil {
		return removed, errors.Join(errors.Join(removalErrors...), err)
	}
	return true, errors.Join(removalErrors...)
}

func darwinCertificatePEM(keychain, fp string) ([]byte, error) {
	raw, err := proc.Command("security", "find-certificate", "-a", "-p", "-c", CommonName, keychain).Output()
	if err != nil {
		return nil, fmt.Errorf("could not export certificate %s from %s: %w", fp, keychain, err)
	}
	return certificatePEMByFingerprint(raw, fp)
}

func certificatePEMByFingerprint(raw []byte, fp string) ([]byte, error) {
	for len(raw) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		candidate := pem.EncodeToMemory(block)
		if SHA1Hex(string(candidate)) == strings.ToUpper(fp) {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("certificate %s was not found in the keychain export", fp)
}

func writeRemovalCertificate(cert []byte) (string, func(), error) {
	file, err := os.CreateTemp("", "care-root-removal-*.crt")
	if err != nil {
		return "", nil, fmt.Errorf("could not prepare certificate trust cleanup: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.Write(cert); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("could not prepare certificate trust cleanup: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("could not prepare certificate trust cleanup: %w", err)
	}
	return path, cleanup, nil
}

func darwinRemoveCertificateCommand(fp, certPath, keychain string, system bool) string {
	args := "security remove-trusted-cert "
	if system {
		args += "-d "
	}
	args += elevate.ShQuote(certPath) + " && security delete-certificate -t -Z " +
		fp + " " + elevate.ShQuote(keychain)
	return args
}

func darwinCARoots(keychain, fp string) ([]string, error) {
	raw, err := proc.Command("security", "find-certificate", "-a", "-Z",
		"-c", CommonName, keychain).Output()
	if err != nil {
		return nil, fmt.Errorf("could not inspect %s: %w", keychain, err)
	}
	hashes := parseSHA1Hashes(string(raw))
	if fp != "" {
		found, err := certInKeychain(keychain, fp)
		if err != nil {
			return nil, err
		}
		if found {
			hashes = appendUnique(hashes, fp)
		}
	}
	return hashes, nil
}

func parseSHA1Hashes(raw string) []string {
	var out []string
	for _, ln := range strings.Split(raw, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "SHA-1 hash:"); ok {
			out = appendUnique(out, rest)
		}
	}
	return out
}

func appendUnique(list []string, h string) []string {
	h = strings.ToUpper(strings.TrimSpace(h))
	if h == "" {
		return list
	}
	for _, existing := range list {
		if existing == h {
			return list
		}
	}
	return append(list, h)
}

func RemoveStepWindows(rootPEM string) (step elevate.Step, need bool, err error) {
	return removeStepWindows(SHA1Hex(rootPEM))
}

func removeStepWindows(fp string) (elevate.Step, bool, error) {
	return removeStepWindowsWith(fp, windowsCARoots, windowsCertificatePresent)
}

func windowsCertificatePresent(fp string) (bool, error) {
	out, err := (proc.Runner{}).Capture("powershell", "-NoProfile", "-Command",
		"$ErrorActionPreference = 'Stop'; Test-Path -LiteralPath "+
			elevate.PSQuote(`Cert:\LocalMachine\Root\`+fp))
	if err != nil {
		return false, fmt.Errorf("could not inspect the Windows root store: %w", err)
	}
	switch strings.TrimSpace(out) {
	case "True":
		return true, nil
	case "False":
		return false, nil
	default:
		return false, errors.New("Windows returned an unknown certificate state")
	}
}

func removeStepWindowsWith(fp string, enumerate func() ([]string, error), contains func(string) (bool, error)) (elevate.Step, bool, error) {
	hashes, err := enumerate()
	if err != nil {
		return elevate.Step{}, false, err
	}
	if fp != "" {
		present, err := contains(fp)
		if err != nil {
			return elevate.Step{}, false, err
		}
		if present {
			hashes = appendUnique(hashes, fp)
		}
	}
	if len(hashes) == 0 {
		return elevate.Step{}, false, nil
	}
	var cmds []string
	for _, hash := range hashes {
		cmds = append(cmds, "Remove-Item -LiteralPath "+
			elevate.PSQuote(`Cert:\LocalMachine\Root\`+hash)+" -ErrorAction Stop")
	}
	return elevate.Step{
		What: "remove CARE's security certificate from this computer",
		PS:   strings.Join(cmds, "; "),
	}, true, nil
}

func removeTrustedRootsWindows(fp string) (bool, error) {
	step, present, err := removeStepWindows(fp)
	if err != nil || !present {
		return false, err
	}
	if err := elevate.Steps([]elevate.Step{step}); err != nil {
		return false, err
	}
	return true, nil
}

func windowsRootPresent(fp string) (bool, error) {
	out, err := proc.Command("certutil", "-store", "Root").Output()
	if err != nil {
		return false, fmt.Errorf("could not inspect the Windows root store: %w", err)
	}
	s := strings.ToUpper(string(out))
	if strings.Contains(s, strings.ToUpper(CommonName)) {
		return true, nil
	}
	return fp != "" && strings.Contains(strings.ReplaceAll(s, " ", ""), strings.ToUpper(fp)), nil
}

func removeTrustedRootsLinux(confirm func(string, string) bool) (bool, error) {
	return removeLinuxAnchors(linuxCAAnchors, linuxTrustBundles, confirm, elevate.Run)
}

func anchorsPresent(anchors []string) (bool, error) {
	for _, p := range anchors {
		_, err := os.Lstat(p)
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("could not inspect %s: %w", p, err)
		}
	}
	return false, nil
}

func bundlesTrust(bundles []string) (bool, error) {
	for _, p := range bundles {
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("could not inspect %s: %w", p, err)
		}
		for len(data) > 0 {
			var block *pem.Block
			block, data = pem.Decode(data)
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			if cert, err := x509.ParseCertificate(block.Bytes); err == nil && cert.Subject.CommonName == CommonName {
				return true, nil
			}
		}
	}
	return false, nil
}

func linuxTrustPresent(anchors, bundles []string) (bool, error) {
	present, err := anchorsPresent(anchors)
	if err != nil || present {
		return present, err
	}
	return bundlesTrust(bundles)
}

func linuxRemovalScript(anchors []string) string {
	paths := make([]string, 0, len(anchors))
	for _, p := range anchors {
		paths = append(paths, elevate.ShQuote(p))
	}
	return "set -e\nrm -f " + strings.Join(paths, " ") + "\nupdated=0\n" +
		"if command -v update-ca-certificates >/dev/null 2>&1; then update-ca-certificates; updated=1; fi\n" +
		"if command -v update-ca-trust >/dev/null 2>&1; then update-ca-trust; updated=1; fi\n" +
		"[ \"$updated\" -eq 1 ]"
}

func removeLinuxAnchors(anchors, bundles []string, confirm func(string, string) bool, run func(string, bool) error) (bool, error) {
	present, err := linuxTrustPresent(anchors, bundles)
	if err != nil || !present {
		return false, err
	}
	sh := linuxRemovalScript(anchors)
	_ = run(sh, false)
	present, err = linuxTrustPresent(anchors, bundles)
	if err != nil {
		return false, err
	}
	if !present {
		return true, nil
	}
	if confirm == nil || !confirm("Remove CARE's certificate?",
		"Remove CARE's security certificate from this computer. Approve the system permission prompt to continue.") {
		return false, nil
	}
	if err := run(sh, true); err != nil {
		return false, err
	}
	present, err = linuxTrustPresent(anchors, bundles)
	if err != nil {
		return false, err
	}
	if present {
		return false, fmt.Errorf("CARE's certificate is still trusted after removal")
	}
	return true, nil
}

func SHA1Hex(pemData string) string {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "" // a CERTIFICATE block with junk DER - not something we installed
	}
	sum := sha1.Sum(block.Bytes)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func certInKeychain(keychain, fp string) (bool, error) {
	out, err := proc.Command("security", "find-certificate", "-a", "-Z", keychain).Output()
	if err != nil {
		return false, fmt.Errorf("could not inspect %s: %w", keychain, err)
	}
	return strings.Contains(strings.ToUpper(string(out)), strings.ToUpper(fp)), nil
}

func logln(log func(string), s string) {
	if log != nil {
		log(s)
	}
}

func Present() bool {
	present, err := Inspect()
	return present || err != nil
}

func Inspect() (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		for _, keychain := range []string{darwinLoginKeychain(), darwinSystemKeychain} {
			hashes, err := darwinCARoots(keychain, "")
			if err != nil || len(hashes) > 0 {
				return len(hashes) > 0, err
			}
		}
		return false, nil
	case "windows":
		return windowsRootPresent("")
	case "linux":
		return linuxTrustPresent(linuxCAAnchors, linuxTrustBundles)
	}
	return false, nil
}
