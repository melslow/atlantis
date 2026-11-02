// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/events/command"
)

// NewPlanJSONStepRunner captures a saved plan as JSON without writing a JSON file.
// Unlike the workflow show step, unavailable JSON is reported as an error.
func NewPlanJSONStepRunner(executor TerraformExec, defaultTfDistribution terraform.Distribution, defaultTFVersion *version.Version) Runner {
	return &planJSONStepRunner{showStepRunner{
		terraformExecutor:     executor,
		defaultTfDistribution: defaultTfDistribution,
		defaultTFVersion:      defaultTFVersion,
	}}
}

type planJSONStepRunner struct {
	showStepRunner
}

func (p *planJSONStepRunner) Run(ctx command.ProjectContext, _ []string, path string, envs map[string]string) (string, error) {
	tfVersion := p.defaultTFVersion
	if ctx.TerraformVersion != nil {
		tfVersion = ctx.TerraformVersion
	}
	if tfVersion.LessThan(version.Must(version.NewVersion(minimumShowTfVersion))) {
		return "", fmt.Errorf("plan JSON requires Terraform %s or later", minimumShowTfVersion)
	}

	remote, err := isRemotePlanFile(GetPlanFilePath(ctx, path))
	if err != nil {
		return "", err
	}
	if remote {
		return "", fmt.Errorf("plan JSON is unavailable for remote operations without a local saved plan")
	}

	// JSON must never be published to the normal project job stream.
	ctx.SuppressJobOutput = true
	output, err := p.show(ctx, path, envs)
	if err != nil {
		return "", err
	}
	output = strings.TrimSpace(output)
	if !strings.HasPrefix(output, "{") || !json.Valid([]byte(output)) {
		return "", fmt.Errorf("terraform show did not produce a valid JSON object")
	}
	return output, nil
}
