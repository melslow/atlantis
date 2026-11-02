// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"errors"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/mocks"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestProjectPlanJSONCapture(t *testing.T) {
	tests := []struct {
		name         string
		includeJSON  bool
		customPlan   bool
		workflowShow bool
		planError    error
		jsonError    error
		wantCapture  bool
	}{
		{name: "default off"},
		{name: "opted in", includeJSON: true, wantCapture: true},
		{name: "compatible custom workflow", includeJSON: true, customPlan: true, wantCapture: true},
		{name: "fresh capture despite workflow show", includeJSON: true, workflowShow: true, wantCapture: true},
		{name: "capture error preserves plan", includeJSON: true, jsonError: errors.New("no saved plan"), wantCapture: true},
		{name: "failed plan skips capture", includeJSON: true, planError: errors.New("plan failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RegisterMockTestingT(t)
			dir := t.TempDir()
			workingDir := mocks.NewMockWorkingDir()
			locker := mocks.NewMockProjectLocker()
			plan := mocks.NewMockStepRunner()
			show := mocks.NewMockStepRunner()
			capture := mocks.NewMockStepRunner()
			customRun := mocks.NewMockCustomStepRunner()
			requirements := mocks.NewMockCommandRequirementHandler()
			workingDirLocker := events.NewDefaultWorkingDirLocker()
			ctx := command.ProjectContext{
				CommandName:       command.Plan,
				Log:               logging.NewNoopLogger(t),
				BaseRepo:          models.Repo{FullName: "org/repo"},
				HeadRepo:          models.Repo{FullName: "org/repo"},
				Pull:              models.PullRequest{Num: 42, BaseRepo: models.Repo{FullName: "org/repo"}},
				RepoRelDir:        ".",
				Workspace:         "production",
				ProjectName:       "app",
				IncludePlanJSON:   tt.includeJSON,
				SuppressJobOutput: true,
				Steps: []valid.Step{
					{StepName: "env", EnvVarName: "TF_VAR_region", EnvVarValue: "eu-west-1"},
					{StepName: "plan"},
				},
			}
			if tt.customPlan {
				ctx.Steps[1] = valid.Step{StepName: "run", RunCommand: "custom plan"}
			}
			if tt.workflowShow {
				ctx.Steps = append(ctx.Steps, valid.Step{StepName: "show"})
			}
			envs := map[string]string{"TF_VAR_region": "eu-west-1"}
			When(workingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](), Any[string]())).
				ThenReturn(dir, nil)
			When(locker.TryLock(Any[logging.SimpleLogging](), Any[models.PullRequest](), Any[models.User](), Any[string](), Any[models.Project](), AnyBool())).
				ThenReturn(&events.TryLockResponse{LockAcquired: true, LockKey: "lock-key", UnlockFn: func() error { return nil }}, nil)
			gitLocked := false
			When(workingDir.GitReadLock(ctx.Pull.BaseRepo, ctx.Pull, ctx.Workspace)).
				Then(func([]Param) ReturnValues {
					gitLocked = true
					return ReturnValues{func() { gitLocked = false }}
				})
			When(plan.Run(ctx, nil, dir, envs)).ThenReturn("Plan: 1 to add, 0 to change, 0 to destroy.", tt.planError)
			When(customRun.Run(ctx, nil, "custom plan", dir, envs, false, nil, nil)).
				ThenReturn("Plan: 1 to add, 0 to change, 0 to destroy.", tt.planError)
			When(show.Run(ctx, nil, dir, envs)).ThenReturn(`{"old":true}`, nil)
			captureCalls := 0
			When(capture.Run(ctx, nil, dir, envs)).Then(func([]Param) ReturnValues {
				captureCalls++
				Assert(t, gitLocked, "checkout read lock must cover capture")
				_, err := workingDirLocker.TryLock(ctx.Pull.BaseRepo.FullName, ctx.Pull.Num, ctx.Workspace, ctx.RepoRelDir, ctx.ProjectName, command.Plan, events.WorkingDirLockMetadata{})
				Assert(t, err != nil, "project execution lock must cover capture")
				return ReturnValues{`{"fresh":true}`, tt.jsonError}
			})
			runner := events.DefaultProjectCommandRunner{
				Locker:                    locker,
				LockURLGenerator:          mockURLGenerator{},
				WorkingDir:                workingDir,
				WorkingDirLocker:          workingDirLocker,
				CommandRequirementHandler: requirements,
				EnvStepRunner:             &runtime.EnvStepRunner{},
				PlanStepRunner:            plan,
				ShowStepRunner:            show,
				PlanJSONStepRunner:        capture,
				RunStepRunner:             customRun,
			}

			result := runner.Plan(ctx)

			Assert(t, !gitLocked, "checkout read lock must be released")
			unlock, err := workingDirLocker.TryLock(ctx.Pull.BaseRepo.FullName, ctx.Pull.Num, ctx.Workspace, ctx.RepoRelDir, ctx.ProjectName, command.Plan, events.WorkingDirLockMetadata{})
			Ok(t, err)
			unlock()
			expectedCalls := 0
			if tt.wantCapture {
				expectedCalls = 1
			}
			Equals(t, expectedCalls, captureCalls)
			if tt.planError != nil {
				Assert(t, result.Error != nil, "expected original plan error")
				Assert(t, result.PlanSuccess == nil, "failed plan must not capture JSON")
				return
			}
			Ok(t, result.Error)
			Equals(t, "", result.Failure)
			Assert(t, result.PlanSuccess != nil, "capture failure must not fail the plan")
			Equals(t, "Plan: 1 to add, 0 to change, 0 to destroy.", result.PlanSuccess.TerraformOutput)
			if tt.jsonError != nil {
				Equals(t, tt.jsonError.Error(), result.PlanSuccess.PlanJSONError)
				Equals(t, 0, len(result.PlanSuccess.PlanJSON))
			} else if tt.wantCapture {
				Equals(t, `{"fresh":true}`, string(result.PlanSuccess.PlanJSON))
				Equals(t, "", result.PlanSuccess.PlanJSONError)
			} else {
				Equals(t, 0, len(result.PlanSuccess.PlanJSON))
				Equals(t, "", result.PlanSuccess.PlanJSONError)
			}
		})
	}
}
