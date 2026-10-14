package clinic

import "github.com/ohcnetwork/care_desktop/app/internal/envconfig"

func (e *Clinic) genSecret() error { return envconfig.EnsureSecrets(e.InstallDir) }
