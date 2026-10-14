package clinic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
)

type projectResource struct {
	kind   string
	query  []string
	remove []string
	ids    []string
}

func (e *Clinic) inspectProject() ([]projectResource, error) {
	label := "label=com.docker.compose.project=" + composeProject
	resources := []projectResource{
		{kind: "containers", query: []string{"ps", "-aq", "--filter", label}, remove: []string{"rm", "-f"}},
		{kind: "volumes", query: []string{"volume", "ls", "-q", "--filter", label}, remove: []string{"volume", "rm"}},
		{kind: "networks", query: []string{"network", "ls", "-q", "--filter", label}, remove: []string{"network", "rm"}},
	}
	var failed []error
	for i := range resources {
		ids, err := e.captureLines(engine.Operation(resources[i].query[0]), resources[i].query[1:]...)
		if err != nil {
			failed = append(failed, fmt.Errorf("could not inspect clinic %s; start Docker and retry: %w", resources[i].kind, err))
		}
		resources[i].ids = ids
	}
	return resources, errors.Join(failed...)
}

func (e *Clinic) TeardownProject() error {
	resources, err := e.inspectProject()
	if err != nil {
		return err
	}
	var failed []error
	for _, resource := range resources {
		if len(resource.ids) == 0 {
			continue
		}
		e.logln("Removing clinic " + resource.kind + "...")
		removeErr := e.run(nil, engine.Operation(resource.remove[0]), append(slices.Clone(resource.remove[1:]), resource.ids...)...)
		left, err := e.captureLines(engine.Operation(resource.query[0]), resource.query[1:]...)
		if err != nil {
			failed = append(failed, errors.Join(removeErr, err))
		} else if len(left) > 0 {
			failed = append(failed, errors.Join(removeErr, fmt.Errorf("%d clinic %s remain", len(left), resource.kind)))
		}
	}
	remaining, err := e.inspectProject()
	if err != nil {
		failed = append(failed, err)
	} else {
		for _, resource := range remaining {
			if len(resource.ids) > 0 {
				failed = append(failed, fmt.Errorf("clinic %s have not been removed", resource.kind))
			}
		}
	}
	return errors.Join(failed...)
}

func looksLikeSourceRepo(dir string) bool {
	for _, marker := range []string{".git", "app", "docs"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
