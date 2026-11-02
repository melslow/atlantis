// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/core/terraform"
	tfclientmocks "github.com/runatlantis/atlantis/server/core/terraform/tfclient/mocks"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestPlanJSONStepRunner(t *testing.T) {
	const planJSON = `{"format_version":"1.2","planned_values":{"outputs":{"large":{"value":9007199254740993}}}}`
	tests := []struct {
		name          string
		output        string
		commandError  error
		planContents  string
		missingPlan   bool
		sharedPlanDir bool
		version       string
		distribution  string
		wantError     string
		wantCommand   bool
	}{
		{name: "local plan", output: planJSON, wantCommand: true},
		{name: "shared plan directory", output: planJSON, sharedPlanDir: true, wantCommand: true},
		{name: "version and OpenTofu overrides", output: planJSON, version: "1.9.0", distribution: "opentofu", wantCommand: true},
		{name: "whitespace", output: "\n " + planJSON + "\n", wantCommand: true},
		{name: "missing plan", missingPlan: true, wantError: "unable to read"},
		{name: "remote plan", planContents: "Atlantis: this plan was created by remote ops\nplan", wantError: "remote operations"},
		{name: "unsupported version", version: "0.11.15", wantError: "requires Terraform 0.12.0"},
		{name: "command error", output: "sensitive-command-output", commandError: errors.New("exit status 1"), wantError: "running terraform show", wantCommand: true},
		{name: "invalid JSON", output: `{"sensitive-value":`, wantError: "valid JSON object", wantCommand: true},
		{name: "array", output: `[]`, wantError: "valid JSON object", wantCommand: true},
		{name: "null", output: `null`, wantError: "valid JSON object", wantCommand: true},
		{name: "empty output", wantError: "valid JSON object", wantCommand: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterMockTestingT(t)
			executor := tfclientmocks.NewMockClient()
			defaultVersion := version.Must(version.NewVersion("1.11.1"))
			distribution := terraform.NewDistribution("terraform")
			runner := runtime.NewPlanJSONStepRunner(executor, distribution, defaultVersion)
			path := t.TempDir()
			ctx := command.ProjectContext{
				CommandName: command.Plan,
				BaseRepo:    models.Repo{FullName: "org/repo"},
				Pull:        models.PullRequest{Num: 42},
				ProjectName: "infra/app",
				RepoRelDir:  ".",
				Workspace:   "production",
				Log:         logging.NewNoopLogger(t),
			}
			expectedVersion := defaultVersion
			expectedBinary := "terraform"
			if tt.version != "" {
				ctx.TerraformVersion = version.Must(version.NewVersion(tt.version))
				expectedVersion = ctx.TerraformVersion
			}
			if tt.distribution != "" {
				ctx.TerraformDistribution = &tt.distribution
				expectedBinary = "tofu"
			}
			if tt.sharedPlanDir {
				ctx.LocalSharePlanDir = t.TempDir()
				Ok(t, runtime.EnsurePlanFileDir(ctx, path))
			}
			planPath := runtime.GetPlanFilePath(ctx, path)
			if !tt.missingPlan {
				Ok(t, os.WriteFile(planPath, []byte(tt.planContents), 0600))
			}
			envs := map[string]string{"TF_VAR_region": "eu-west-1"}
			calls := 0
			When(executor.RunCommandWithVersion(
				Any[command.ProjectContext](), Any[string](), Any[[]string](),
				Any[map[string]string](), Any[terraform.Distribution](), Any[*version.Version](), Any[string](),
			)).Then(func(args []Param) ReturnValues {
				calls++
				expectedCtx := ctx
				expectedCtx.SuppressJobOutput = true
				Equals(t, expectedCtx, args[0])
				Equals(t, path, args[1])
				Equals(t, []string{"show", "-json", filepath.Clean(planPath)}, args[2])
				Equals(t, envs, args[3])
				Equals(t, expectedBinary, args[4].(terraform.Distribution).BinName())
				Equals(t, expectedVersion, args[5])
				Equals(t, ctx.Workspace, args[6])
				return ReturnValues{tt.output, tt.commandError}
			})

			output, err := runner.Run(ctx, nil, path, envs)

			if tt.wantError == "" {
				Ok(t, err)
				Equals(t, planJSON, output)
			} else {
				Assert(t, err != nil, "expected capture error")
				Assert(t, strings.Contains(err.Error(), tt.wantError), "unexpected error %q", err)
				Assert(t, !strings.Contains(err.Error(), "sensitive"), "error must not expose command output")
				Equals(t, "", output)
			}
			expectedCalls := 0
			if tt.wantCommand {
				expectedCalls = 1
			}
			Equals(t, expectedCalls, calls)
			_, err = os.Stat(filepath.Join(path, ctx.GetShowResultFileName()))
			Assert(t, os.IsNotExist(err), "capture must not write a JSON file")
		})
	}
}
