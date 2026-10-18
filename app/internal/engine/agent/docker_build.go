package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// backendVenv is the virtualenv CARE's backend builder and runtime images
// share (prod.Dockerfile); derived images replace it with the builder's.
const (
	backendVenv   = "/app/.venv"
	buildStageTag = "care-clinic/build-input"
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

// ImageBuild bakes op.Plugins into a derived backend image: the builder
// pip-installs them into its virtualenv, which replaces the base image's (see
// buildDockerfile). It returns the image ID,
// tagged <kind repo>:<key prefix>. An image already
// built from the same inputs is reused. Building needs internet access for
// pip and git.
func (d *DockerExecutor) ImageBuild(ctx context.Context, op protocol.ImageBuild, em Emitter) (protocol.ImageBuildResult, error) {
	if op.Kind != protocol.BuildBackendPlugins {
		// Frontend plugins are registered by CARE admins (PlugConfig).
		return protocol.ImageBuildResult{}, ErrNotImplemented
	}
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
	if _, err := d.stream(ctx, em, "image build", "", Command{Args: args, Env: env, Stdin: strings.NewReader(df)}); err != nil {
		return protocol.ImageBuildResult{}, err
	}
	info, err := d.inspectImage(ctx, tag)
	if err != nil {
		return protocol.ImageBuildResult{}, err
	}
	em.Progress(protocol.Progress{State: "built", Message: tag})
	return protocol.ImageBuildResult{Ref: op.Kind.Repo(), Digest: protocol.Digest(info.ID)}, nil
}
