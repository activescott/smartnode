package config

import (
	"strings"

	"github.com/rocket-pool/smartnode/shared/services/config"
	cfgtypes "github.com/rocket-pool/smartnode/shared/types/config"
)

func createMevLocalStep(wiz *wizard, currentStep int, totalSteps int) *checkBoxWizardStep {

	// Create the labels
	regulatedAllLabel := strings.TrimPrefix(wiz.md.Config.MevBoost.EnableRegulatedAllMev.Name, "Enable ")
	regulatedNoSandwichLabel := strings.TrimPrefix(wiz.md.Config.MevBoost.EnableRegulatedNoSandwich.Name, "Enable ")
	unregulatedAllLabel := strings.TrimPrefix(wiz.md.Config.MevBoost.EnableUnregulatedAllMev.Name, "Enable ")
	unregulatedNoSandwichLabel := strings.TrimPrefix(wiz.md.Config.MevBoost.EnableUnregulatedNoSandwich.Name, "Enable ")

	helperText := "By default, your Smartnode has MEV-Boost enabled. This allows you to capture extra profits from block proposals. Select the profiles you would like to enable below. Leave all options unchecked if you wish to opt out of MEV-Boost for now, [orange]but it will be required in the future.[white]\n\n[lime]Please read our guide to learn more about MEV:\nhttps://docs.rocketpool.net/guides/node/mev.html\n"

	show := func(modal *checkBoxModalLayout) {
		labels, descriptions, selections := getMevChoices(wiz.md.Config.MevBoost)
		modal.generateCheckboxes(labels, descriptions, selections)

		wiz.md.setPage(modal.page)
		modal.focus()
	}

	done := func(choices map[string]bool) {
		wiz.md.Config.MevBoost.SelectionMode.Value = cfgtypes.MevSelectionMode_Profile
		wiz.md.Config.EnableMevBoost.Value = false

		atLeastOneEnabled := false
		enabled, exists := choices[regulatedAllLabel]
		if exists {
			wiz.md.Config.MevBoost.EnableRegulatedAllMev.Value = enabled
			atLeastOneEnabled = atLeastOneEnabled || enabled
		}
		enabled, exists = choices[regulatedNoSandwichLabel]
		if exists {
			wiz.md.Config.MevBoost.EnableRegulatedNoSandwich.Value = enabled
			atLeastOneEnabled = atLeastOneEnabled || enabled
		}
		enabled, exists = choices[unregulatedAllLabel]
		if exists {
			wiz.md.Config.MevBoost.EnableUnregulatedAllMev.Value = enabled
			atLeastOneEnabled = atLeastOneEnabled || enabled
		}
		enabled, exists = choices[unregulatedNoSandwichLabel]
		if exists {
			wiz.md.Config.MevBoost.EnableUnregulatedNoSandwich.Value = enabled
			atLeastOneEnabled = atLeastOneEnabled || enabled
		}

		wiz.md.Config.EnableMevBoost.Value = atLeastOneEnabled
		wiz.finishedModal.show()
	}

	back := func() {
		wiz.metricsModal.show()
	}

	return newCheckBoxStep(
		wiz,
		currentStep,
		totalSteps,
		helperText,
		90,
		"MEV-Boost",
		show,
		done,
		back,
		"step-mev-local",
	)

}

func getMevChoices(config *config.MevBoostConfig) ([]string, []string, []bool) {
	labels := []string{}
	descriptions := []string{}
	settings := []bool{}

	regulatedAllMev, regulatedNoSandwich, unregulatedAllMev, unregulatedNoSandwich := config.GetAvailableProfiles()

	if regulatedAllMev {
		label := strings.TrimPrefix(config.EnableRegulatedAllMev.Name, "Enable ")
		labels = append(labels, label)
		descriptions = append(descriptions, getDescriptionBody(config.EnableRegulatedAllMev.Description))
		settings = append(settings, config.EnableRegulatedAllMev.Value.(bool))
	}
	if regulatedNoSandwich {
		label := strings.TrimPrefix(config.EnableRegulatedNoSandwich.Name, "Enable ")
		labels = append(labels, label)
		descriptions = append(descriptions, getDescriptionBody(config.EnableRegulatedNoSandwich.Description))
		settings = append(settings, config.EnableRegulatedNoSandwich.Value.(bool))
	}
	if unregulatedAllMev {
		label := strings.TrimPrefix(config.EnableUnregulatedAllMev.Name, "Enable ")
		labels = append(labels, label)
		descriptions = append(descriptions, getDescriptionBody(config.EnableUnregulatedAllMev.Description))
		settings = append(settings, config.EnableUnregulatedAllMev.Value.(bool))
	}
	if unregulatedNoSandwich {
		label := strings.TrimPrefix(config.EnableUnregulatedNoSandwich.Name, "Enable ")
		labels = append(labels, label)
		descriptions = append(descriptions, getDescriptionBody(config.EnableUnregulatedNoSandwich.Description))
		settings = append(settings, config.EnableUnregulatedNoSandwich.Value.(bool))
	}

	return labels, descriptions, settings
}

func getDescriptionBody(description string) string {
	index := strings.Index(description, "Select this")
	return description[index:]
}
