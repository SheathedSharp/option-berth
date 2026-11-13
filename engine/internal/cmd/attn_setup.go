package cmd

// The guided half of `oberth attn config`: a person at a terminal picks a
// provider, then a model, then types the key, instead of remembering three
// flags. The flag form stays the script and CI path — this file only chooses
// the same fields, and both forms end in attnCommitConfig, so neither can skip
// the probe that proves the key answers.
//
// The two extra rows (a custom endpoint and a custom model) exist because no
// catalog is complete: a vendor list is a convenience, and the person must
// always be able to point the adapter somewhere it has never heard of.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/terminal"
	"github.com/spf13/cobra"
)

// The values the two "none of these" rows carry. They never reach the settings
// file: choosing one asks for the field instead of storing a label.
const (
	attnCustomEndpoint = "custom-endpoint"
	attnCustomModel    = "custom-model"
)

// attnConfigWizardApplies is the whole gate for the guided form: a person at a
// terminal, no explicit field, and no JSON. Any flag means the caller already
// decided; a pipe or a Windows build has no picker to draw.
func attnConfigWizardApplies() bool {
	return !attnJSONFlag && noAttnConfigFlags() && terminal.Interactive()
}

func runAttnConfigWizard(cmd *cobra.Command) error {
	current, _, err := model.LoadLocalConfig()
	if err != nil {
		// A malformed file is replaceable: config is the repair path, the same
		// way the flag form treats it.
		current = model.LocalConfig{}
	}
	next, err := attnAskProvider(current)
	if err != nil {
		return attnWizardStop(cmd, err)
	}
	key, err := attnAskKey(next.APIKey)
	if err != nil {
		return attnWizardStop(cmd, err)
	}
	next.APIKey = key
	return attnCommitConfig(cmd, next)
}

// attnWizardStop turns a cancelled question into "nothing was changed" rather
// than an error: walking away from a setup prompt is a choice, not a failure.
func attnWizardStop(cmd *cobra.Command, err error) error {
	if errors.Is(err, terminal.ErrCancelled) {
		fmt.Fprintln(cmd.OutOrStdout(), "cancelled; nothing was changed")
		return nil
	}
	return err
}

// attnProviderChoices is the provider list and the row that should start
// highlighted: the stored vendor when one is known, the custom row for an
// endpoint no preset names, and the first row for a fresh setup.
func attnProviderChoices(current model.LocalConfig) ([]terminal.Choice, int) {
	vendors := model.Vendors()
	choices := make([]terminal.Choice, 0, len(vendors)+1)
	initial := -1
	for _, vendor := range vendors {
		if vendor.Name == current.Preset {
			initial = len(choices)
		}
		choices = append(choices, terminal.Choice{
			Value: vendor.Name, Label: vendor.Label, Detail: vendor.Summary,
		})
	}
	choices = append(choices, terminal.Choice{
		Value: attnCustomEndpoint, Label: "Custom endpoint",
		Detail: "any endpoint that speaks the decisions protocol",
	})
	if initial < 0 {
		if current.Endpoint == "" {
			initial = 0
		} else {
			// A stored endpoint that no preset names is a custom deployment:
			// land on the row that keeps it.
			initial = len(choices) - 1
		}
	}
	return choices, initial
}

func attnAskProvider(current model.LocalConfig) (model.LocalConfig, error) {
	next := current
	next.Schema = model.LocalConfigSchema

	choices, initial := attnProviderChoices(current)
	picked, err := terminal.Select(os.Stderr, "Provider", choices, initial)
	if err != nil {
		return next, err
	}

	if choices[picked].Value == attnCustomEndpoint {
		endpoint, err := terminal.Line(os.Stderr, "Endpoint URL", current.Endpoint)
		if err != nil {
			return next, err
		}
		next.Preset = ""
		next.Endpoint = strings.TrimSpace(endpoint)
		modelName, err := terminal.Line(os.Stderr, "Model name", current.Model)
		if err != nil {
			return next, err
		}
		next.Model = strings.TrimSpace(modelName)
		return next, nil
	}

	vendor, ok := model.VendorByName(choices[picked].Value)
	if !ok {
		return next, fmt.Errorf("attn: provider %q is not in the catalog", choices[picked].Value)
	}
	next.Preset, next.Endpoint = vendor.Name, vendor.Endpoint
	modelName, err := attnAskModel(vendor, current)
	if err != nil {
		return next, err
	}
	next.Model = modelName
	return next, nil
}

// attnModelChoices is the model list for one vendor plus the custom row. A
// stored model that this vendor does not list starts on the custom row, so
// opening the setup never silently replaces a working choice.
func attnModelChoices(vendor model.Vendor, current model.LocalConfig) ([]terminal.Choice, int) {
	choices := make([]terminal.Choice, 0, len(vendor.Models)+1)
	initial := 0
	for _, candidate := range vendor.Models {
		if candidate.ID == current.Model {
			initial = len(choices)
		}
		choices = append(choices, terminal.Choice{
			Value: candidate.ID, Label: candidate.Label, Detail: candidate.Note,
		})
	}
	choices = append(choices, terminal.Choice{
		Value: attnCustomModel, Label: "Custom model",
		Detail: "type the model name this endpoint expects",
	})
	if current.Model != "" && !vendorHasModel(vendor, current.Model) {
		initial = len(choices) - 1
	}
	return choices, initial
}

func attnAskModel(vendor model.Vendor, current model.LocalConfig) (string, error) {
	choices, initial := attnModelChoices(vendor, current)
	picked, err := terminal.Select(os.Stderr, "Model", choices, initial)
	if err != nil {
		return "", err
	}
	if choices[picked].Value == attnCustomModel {
		return terminal.Line(os.Stderr, "Model name", current.Model)
	}
	return choices[picked].Value, nil
}

func vendorHasModel(vendor model.Vendor, id string) bool {
	for _, candidate := range vendor.Models {
		if candidate.ID == id {
			return true
		}
	}
	return false
}

// attnAskKey reads the secret, keeping a stored one on an empty answer. The
// label says which of the two it will do, so Enter is never a surprise.
func attnAskKey(stored string) (string, error) {
	label := "API key (input hidden, enter keeps the stored key)"
	if strings.TrimSpace(stored) == "" {
		label = "API key (input hidden)"
	}
	key, err := terminal.ReadSecret(os.Stderr, label)
	if err != nil {
		return "", err
	}
	if key == "" {
		if strings.TrimSpace(stored) == "" {
			return "", usageError{errors.New("no key was entered; rerun `oberth attn config` when you have one")}
		}
		return stored, nil
	}
	return key, nil
}

// attnProviderList is what the non-interactive path shows when nothing is
// configured: the same vendors and models the picker would offer, so a script
// or Windows reader can copy the fields instead of guessing them.
func attnProviderList() string {
	var b strings.Builder
	b.WriteString("  next      run `oberth attn config` in a terminal to choose, or pass\n")
	b.WriteString("            --preset / --endpoint / --model.\n")
	for _, vendor := range model.Vendors() {
		fmt.Fprintf(&b, "\n  %s · %s\n    %s\n", vendor.Name, vendor.Summary, vendor.Endpoint)
		for _, candidate := range vendor.Models {
			fmt.Fprintf(&b, "      %-32s %s\n", candidate.ID, candidate.Label)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
