package compose

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/careversions"
)

var stageMu sync.Mutex

type Update struct {
	Backend  string `json:"backend"`
	Frontend string `json:"frontend"`
}

func (u Update) Any() bool { return u.Backend != "" || u.Frontend != "" }

func Short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// PrepareUpdate checks CARE 3.1 for newer images and
// downloads them as "-next" images, ready to apply.
func (b *Builder) PrepareUpdate() (Update, error) {
	if b.stopped() {
		return Update{}, nil
	}
	m, err := careversions.Fetch(b.dir, b.set)
	if err != nil {
		return Update{}, err
	}
	return b.PrepareUpdateManifest(m)
}

func (b *Builder) PrepareUpdateManifest(m *careversions.Manifest) (Update, error) {
	if b.stopped() {
		return Update{}, nil
	}
	if m == nil {
		return Update{}, errors.New("verified CARE image manifest is required")
	}
	lock := ReadLock(b.dir)
	if lock.Backend.Current.Image == "" || lock.Frontend.Current.Image == "" {
		return Update{}, nil
	}
	staged := map[string]Release{}
	if wanted(lock.Backend, m.Backend) {
		staged[Backend] = m.Backend
	}
	if wanted(lock.Frontend, m.Frontend) {
		staged[Frontend] = m.Frontend
	}
	return b.stage(staged)
}

func (b *Builder) stage(staged map[string]Release) (Update, error) {
	stageMu.Lock()
	defer stageMu.Unlock()
	var u Update
	next := b.staging(staged)
	for _, s := range []struct {
		service string
		ensure  func() error
		dst     *string
	}{
		{Frontend, next.EnsureFrontendImage, &u.Frontend},
		{Backend, next.EnsureBackendImage, &u.Backend},
	} {
		rel, ok := staged[s.service]
		if !ok {
			continue
		}
		if next.stopped() {
			return Update{}, errors.New("the CARE update download was interrupted")
		}
		b.logln("A newer CARE " + s.service + " is available (" + Short(rel.Commit) + ") - downloading it in the background.")
		if err := s.ensure(); err != nil {
			return Update{}, err
		}
		if next.stopped() {
			return Update{}, errors.New("the CARE update download was interrupted")
		}
		if err := b.updateLock(func(l *Lock) {
			c := l.Get(s.service)
			r := rel
			c.Next = &r
			l.Set(s.service, c)
		}); err != nil {
			return Update{}, err
		}
		*s.dst = rel.Commit
	}
	return u, nil
}

func wanted(c Channel, rel Release) bool {
	return rel.Commit != "" && rel.Image != c.Current.Image && rel.Commit != c.Declined
}

func (b *Builder) Waiting() Update {
	lock := ReadLock(b.dir)
	u := Update{}
	if lock.Backend.Next != nil {
		u.Backend = lock.Backend.Next.Commit
	}
	if lock.Frontend.Next != nil {
		u.Frontend = lock.Frontend.Next.Commit
	}
	return u
}

func (b *Builder) ApplyPending() (Update, error) {
	stageMu.Lock()
	defer stageMu.Unlock()
	var applied Update
	var retagged []string
	err := ModifyLock(b.dir, func(lock *Lock) error {
		return b.applyPending(lock, &applied, &retagged)
	})
	if err != nil {
		if rollbackErr := b.restoreStableTags(retagged); rollbackErr != nil {
			return applied, fmt.Errorf("%w; restoring previous CARE image tags also failed: %v", err, rollbackErr)
		}
	}
	return applied, err
}

func (b *Builder) applyPending(lock *Lock, applied *Update, retagged *[]string) error {
	for _, s := range []struct {
		service string
		image   string
		dst     *string
	}{
		{Backend, b.set.BackendImage, &applied.Backend},
		{Frontend, b.set.FrontendImage, &applied.Frontend},
	} {
		c := lock.Get(s.service)
		if c.Next == nil {
			continue
		}
		staged := s.image + "-next"
		_, ok, err := b.builtFrom(staged)
		if err != nil {
			return err
		}
		if !ok {
			c.Next, c.Declined = nil, ""
			lock.Set(s.service, c)
			continue
		}
		previousTag := previousImageTag(s.image)
		if c.Current.Image != "" {
			if err := b.run.Run("docker", "image", "tag", s.image, previousTag); err != nil {
				return fmt.Errorf("could not retain the previous CARE %s image: %w", s.service, err)
			}
		}
		if err := b.run.Run("docker", "image", "tag", staged, s.image); err != nil {
			if c.Current.Image != "" {
				if restoreErr := b.run.Run("docker", "image", "tag", previousTag, s.image); restoreErr != nil {
					return fmt.Errorf("could not activate CARE %s: %w; restoring its previous tag failed: %v", s.service, err, restoreErr)
				}
			}
			return err
		}
		b.logln("CARE " + s.service + " updated to " + Short(c.Next.Commit) + ".")
		*s.dst = c.Next.Commit
		previous := c.Current
		c.Current, c.Previous, c.Next, c.Declined = *c.Next, &previous, nil, ""
		*retagged = append(*retagged, s.image)
		lock.Set(s.service, c)
	}
	return nil
}

func previousImageTag(image string) string { return image + "-previous" }

func (b *Builder) restoreStableTags(images []string) error {
	var failures []error
	for i := len(images) - 1; i >= 0; i-- {
		if err := b.run.Run("docker", "image", "tag", previousImageTag(images[i]), images[i]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// RollbackApplied restores the previous local image tags and lock entries.
// Kept image IDs remain available for retry or support investigation.
func (b *Builder) RollbackApplied() error {
	stageMu.Lock()
	defer stageMu.Unlock()
	err := ModifyLock(b.dir, func(lock *Lock) error {
		for _, item := range []struct {
			service string
			image   string
		}{{Backend, b.set.BackendImage}, {Frontend, b.set.FrontendImage}} {
			channel := lock.Get(item.service)
			if channel.Previous == nil {
				continue
			}
			current := channel.Current
			if err := b.run.Run("docker", "image", "tag", previousImageTag(item.image), item.image); err != nil {
				return fmt.Errorf("could not restore the previous CARE %s image: %w", item.service, err)
			}
			channel.Current, channel.Next, channel.Previous, channel.Declined =
				*channel.Previous, &current, nil, ""
			lock.Set(item.service, channel)
		}
		return nil
	})
	return err
}

func (b *Builder) DeclineUpdate() error {
	stageMu.Lock()
	defer stageMu.Unlock()
	return ModifyLock(b.dir, func(lock *Lock) error {
		for _, service := range []string{Backend, Frontend} {
			c := lock.Get(service)
			if c.Next != nil {
				c.Declined = c.Next.Commit
			}

			lock.Set(service, c)
		}
		return nil
	})
}

// DiscardPending removes the prepared update.
// Declining a routine update intentionally keeps it for the next clinic start.
func (b *Builder) DiscardPending() error {
	stageMu.Lock()
	defer stageMu.Unlock()
	return ModifyLock(b.dir, func(lock *Lock) error {
		for _, service := range []string{Backend, Frontend} {
			c := lock.Get(service)
			c.Next = nil
			lock.Set(service, c)
		}
		return nil
	})
}
