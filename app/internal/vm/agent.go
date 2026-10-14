package vm

import "github.com/ohcnetwork/care_desktop/app/internal/engine"

func ConfigureAgent() {
	engine.Default = engine.New(dialAgent)
}

func init() { ConfigureAgent() }
