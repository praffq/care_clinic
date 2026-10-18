package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// backendVenv is the virtualenv CARE's backend builder and runtime images
// share (prod.Dockerfile); derived images replace it with the builder's.
const (
	backendVenv   = "/app/.venv"
	buildStageTag = "care-clinic/build-input"
	// The frontend builder holds care_fe at /app (npm run build writes
	// /app/build); the published image serves /usr/share/nginx/html with
	// prebaked remotes under plugins/ (deployments/frontend.Dockerfile).
	frontendApp     = "/app"
	frontendHTML    = "/usr/share/nginx/html"
	frontendPlugins = frontendHTML + "/plugins"
)

// BuildKey identifies a derived image: the same inputs give the same key,
// so an existing image with that key is reused instead of rebuilt.
func BuildKey(op protocol.ImageBuild) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode([]any{op.Kind, op.Base, op.Builder, op.Plugins})
	for _, k := range slices.Sorted(maps.Keys(op.BuildEnv)) {
		_ = enc.Encode([]string{k, op.BuildEnv[k]})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pipSpec is what pip installs for a CARE Plug: package plus version, the
// same concatenation CARE's PlugManager.install uses.
func pipSpec(p protocol.PluginSpec) string { return p.Package + p.Version }

func execForm(args ...string) string {
	b, _ := json.Marshal(args)
	return string(b)
}

// buildDockerfile renders the Dockerfile for op. builder and base are local
// tags of the input images. Every RUN uses the exec form and every value
// was validated by protocol.ImageBuild, so nothing reaches a shell.
func buildDockerfile(op protocol.ImageBuild, builder, base string) string {
	if op.Kind == protocol.BuildFrontendRebuild {
		return frontendDockerfile(op, builder, base)
	}
	var b strings.Builder
	envKeys := slices.Sorted(maps.Keys(op.BuildEnv))
	b.WriteString("FROM " + builder + " AS plugins\n")
	for _, k := range envKeys {
		b.WriteString("ARG " + k + "\n")
	}
	args := []string{"python", "-m", "pip", "install", "--no-cache-dir", "--disable-pip-version-check"}
	for _, p := range op.Plugins {
		args = append(args, pipSpec(p))
	}
	b.WriteString("RUN " + execForm(args...) + "\n")
	b.WriteString("FROM " + base + "\n")
	b.WriteString("COPY --from=plugins --chown=django:django " + backendVenv + " " + backendVenv + "\n")
	return b.String()
}

// frontendDockerfile rebuilds care_fe with op.BuildEnv (passed as build
// args, so Vite reads them from the environment ahead of care_fe's .env
// files) and swaps the result into base, keeping base's prebaked remotes.
func frontendDockerfile(op protocol.ImageBuild, builder, base string) string {
	var b strings.Builder
	b.WriteString("FROM " + base + " AS published\n")
	b.WriteString("FROM " + builder + " AS app\n")
	for _, k := range slices.Sorted(maps.Keys(op.BuildEnv)) {
		b.WriteString("ARG " + k + "\n")
	}
	b.WriteString("WORKDIR " + frontendApp + "\n")
	b.WriteString("RUN " + execForm("npm", "run", "build") + "\n")
	b.WriteString("RUN " + execForm("rm", "-rf", frontendApp+"/build/plugins") + "\n")
	b.WriteString("COPY --from=published " + frontendPlugins + " " + frontendApp + "/build/plugins\n")
	b.WriteString("FROM " + base + "\n")
	b.WriteString("RUN " + execForm("rm", "-rf", frontendHTML) + "\n")
	b.WriteString("COPY --from=app " + frontendApp + "/build " + frontendHTML + "\n")
	return b.String()
}

type buildImageInfo struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// resolveInput finds a digest-pinned input image in the engine: by its
// reference, or by image ID (dev tarballs pin the ID as the digest).
func (d *DockerExecutor) resolveInput(ctx context.Context, ref string) (string, error) {
	if info, err := d.inspectImage(ctx, ref); err == nil {
		return info.ID, nil
	}
	_, dg, _ := strings.Cut(ref, "@")
	info, err := d.inspectImage(ctx, dg)
	if err != nil {
		return "", &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "image " + ref + " is not in the engine; load or pull it first"}
	}
	return info.ID, nil
}

// ImageBuild builds a derived image (see buildDockerfile). backend-plugins:
// the builder pip-installs op.Plugins into its virtualenv, which replaces the
// base image's; needs internet access for pip and git. frontend-rebuild: the
// builder runs care_fe's build with op.BuildEnv and the result replaces the
// base image's app; offline. It returns the image ID, tagged
// <kind repo>:<key prefix>. An image already built from the same inputs is
// reused. A failure that retrying cannot fix is a CodeBuildFailed OpError.
func (d *DockerExecutor) ImageBuild(ctx context.Context, op protocol.ImageBuild, em Emitter) (protocol.ImageBuildResult, error) {
	key := BuildKey(op)
	tag := op.Kind.Repo() + ":" + key[:12]
	if out, err := d.capture(ctx, "image inspect", Command{Args: []string{dockerBin, "image", "inspect", "--format", "{{json .}}", tag}}); err == nil {
		var info buildImageInfo
		if json.Unmarshal(bytes.TrimSpace(out), &info) == nil && info.Config.Labels[protocol.BuildKeyLabel] == key {
			em.Progress(protocol.Progress{State: "cached", Message: tag})
			return protocol.ImageBuildResult{Ref: op.Kind.Repo(), Digest: protocol.Digest(info.ID)}, nil
		}
	}

	em.Progress(protocol.Progress{State: "resolving", Message: op.Base})
	inputs := map[string]string{"builder": op.Builder, "base": op.Base}
	tags := map[string]string{}
	for _, name := range []string{"builder", "base"} {
		id, err := d.resolveInput(ctx, inputs[name])
		if err != nil {
			return protocol.ImageBuildResult{}, err
		}
		t := buildStageTag + ":" + name + "-" + key[:12]
		if _, err := d.capture(ctx, "image tag", Command{Args: []string{dockerBin, "image", "tag", id, t}}); err != nil {
			return protocol.ImageBuildResult{}, err
		}
		tags[name] = t
	}
	defer func() {
		_ = d.run(context.WithoutCancel(ctx), Command{Args: []string{dockerBin, "image", "rm", "--no-prune", tags["builder"], tags["base"]}, Stdout: io.Discard, Stderr: io.Discard})
	}()

	df := buildDockerfile(op, tags["builder"], tags["base"])
	args := []string{dockerBin, "build", "--progress", "plain", "--pull=false",
		"--tag", tag, "--label", protocol.BuildKeyLabel + "=" + key}
	var env []string
	for _, k := range slices.Sorted(maps.Keys(op.BuildEnv)) {
		// The value comes from the environment, so it never appears in argv.
		args = append(args, "--build-arg", k)
		env = append(env, k+"="+op.BuildEnv[k])
	}
	args = append(args, "-")
	em.Progress(protocol.Progress{State: "building", Message: tag})
	if err := d.build(ctx, em, Command{Args: args, Env: env, Stdin: strings.NewReader(df)}); err != nil {
		return protocol.ImageBuildResult{}, err
	}
	info, err := d.inspectImage(ctx, tag)
	if err != nil {
		return protocol.ImageBuildResult{}, err
	}
	em.Progress(protocol.Progress{State: "built", Message: tag})
	return protocol.ImageBuildResult{Ref: op.Kind.Repo(), Digest: protocol.Digest(info.ID)}, nil
}

// buildTail is how many trailing output lines classify a failed build.
const buildTail = 400

// build runs docker build, classifying a failure (see classifyBuild).
func (d *DockerExecutor) build(ctx context.Context, em Emitter, c Command) error {
	out := &lineWriter{em: em, stream: "stdout", tail: buildTail}
	errw := &lineWriter{em: em, stream: "stderr", tail: buildTail}
	c.Stdout, c.Stderr = out, errw
	err := d.run(ctx, c)
	out.flush()
	errw.flush()
	if err == nil {
		return nil
	}
	cerr := commandError(ctx, "image build", err, errw.last)
	if ctx.Err() != nil {
		return cerr
	}
	if reason, ok := classifyBuild(append(out.lines, errw.lines...)); ok {
		return &protocol.OpError{Code: protocol.CodeBuildFailed, Message: "image build failed: " + reason}
	}
	return cerr
}

// Output that marks a failed build as transient: the network, a registry or
// the machine, not the inputs. It wins over a permanent marker, since pip
// reports "no matching distribution" when the index is unreachable.
var transientBuild = []string{
	"temporary failure in name resolution", "could not resolve host", "name or service not known",
	"connection reset", "connection refused", "connection timed out", "network is unreachable",
	"read timed out", "timed out", "tls handshake timeout", "etimedout", "econnreset", "econnrefused",
	"eai_again", "enotfound", "socket hang up", "max retries exceeded", "502 bad gateway",
	"503 service unavailable", "504 gateway", "429 too many requests", "no space left on device",
	"cannot allocate memory", "killed", "out of memory", "heap out of memory",
}

// Output that marks a failed build as deterministic: the same inputs fail
// the same way, so retrying cannot help.
var permanentBuild = []string{
	// pip
	"no matching distribution found", "could not find a version that satisfies",
	"resolutionimpossible", "subprocess-exited-with-error", "metadata-generation-failed",
	"invalid requirement", "conflicting dependencies", "requires a different python",
	// git (a missing GitHub repo asks for credentials)
	"repository not found", "could not read username", "did not match any file(s) known to git",
	"remote branch", "couldn't find remote ref", "invalid reference",
	// npm, vite, typescript
	"npm err! 404", "npm error 404", "npm err! code e404", "npm error code e404", "etarget",
	"eresolve", "error during build", "rollup failed", "build failed with", "error ts",
	"syntaxerror", "could not resolve", "is not exported by",
	"care-build-remote: invalid",
}

// classifyBuild reports whether a failed build's output shows a
// deterministic failure, with the line that shows it.
func classifyBuild(lines []string) (string, bool) {
	var hit string
	for _, l := range lines {
		low := strings.ToLower(l)
		for _, m := range transientBuild {
			if strings.Contains(low, m) {
				return "", false
			}
		}
		if hit == "" {
			for _, m := range permanentBuild {
				if strings.Contains(low, m) {
					hit = l
					break
				}
			}
		}
	}
	if hit == "" {
		return "", false
	}
	hit = strings.TrimSpace(buildLinePrefix.ReplaceAllString(hit, ""))
	if len(hit) > 300 {
		hit = hit[:300] + "…"
	}
	return hit, true
}

// buildLinePrefix is buildkit's "#12 3.456 " step prefix.
var buildLinePrefix = regexp.MustCompile(`^#\d+ [0-9.]+ `)
