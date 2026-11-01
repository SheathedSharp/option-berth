package doctor

// The optional assessment gets one offline row. It answers "is this turned on,
// and if not, can it be?" without contacting a provider: `doctor` is a setup
// check, and a network call inside it would make a slow or unreachable provider
// look like a broken installation.

import (
	"context"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/jev"
)

func checkJev(_ context.Context, env *Env) rpc.DoctorCheck {
	// The settings live under the same home every other check reads, so this
	// row describes the installation being examined rather than whatever home
	// the process happens to have.
	cfg, present, err := jev.LoadLocalJevConfigFrom(env.SettingsPath)
	if err != nil {
		// A malformed file is the one state a person cannot discover from the
		// outside: it is present, so nothing falls back, and the reason is only
		// visible here.
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: "Jev settings are not usable",
			Detail:  err.Error(),
			Fix:     "run `oberth jev status` to see the resolved state, then `oberth jev enable` to rewrite it",
		}
	}
	// OneShot is deliberately empty: this row reports the committed setting, so
	// an unrelated BERTH_JEV export cannot make doctor claim a provider is
	// configured.
	consent := jev.Resolve(jev.ResolveInput{Settings: cfg, SettingsPresent: present})
	if !consent.Enabled {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "Jev: off (optional)",
			Detail:  "the optional model assessment is not enabled; runtime facts, services and every control command work without it",
			Fix:     "run `oberth jev enable` to turn it on, or ignore this row",
		}
	}
	if consent.Key == "" {
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: "Jev: on, but no usable key",
			Detail:  "enabled in settings but no key resolved, so every assessment will report no_key",
			Fix:     "run `oberth jev enable` to store a key, or `oberth jev test` to see the current reason",
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: "Jev: on · " + consent.Provider,
		Detail:  "endpoint " + consent.Endpoint + ", key from " + string(consent.KeySource),
	}
}
