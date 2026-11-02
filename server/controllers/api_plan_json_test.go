// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/controllers"
	"github.com/runatlantis/atlantis/server/core/drift"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/webhooks"
	. "github.com/runatlantis/atlantis/testing"
)

func TestAPIController_DetectDrift_PlanJSON(t *testing.T) {
	const planJSON = `{"format_version":"1.2","secret":"plan-json-secret","large":9007199254740993}`
	const jsonError = "capture-error-only"
	tests := []struct {
		name          string
		includeJSON   bool
		includeOutput bool
		captureFailed bool
		planFailed    bool
		policyFailed  bool
		noChanges     bool
	}{
		{name: "default off"},
		{name: "text only", includeOutput: true},
		{name: "JSON only", includeJSON: true},
		{name: "text and JSON", includeJSON: true, includeOutput: true},
		{name: "capture failure is not plan failure", includeJSON: true, captureFailed: true},
		{name: "capture error omitted without opt-in", captureFailed: true},
		{name: "plan failure omits JSON", includeJSON: true, planFailed: true},
		{name: "policy failure preserves plan JSON", includeJSON: true, policyFailed: true},
		{name: "successful no-change plan includes JSON", includeJSON: true, noChanges: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac, builder, runner := setup(t)
			storage := drift.NewInMemoryStorage()
			ac.DriftStorage = storage
			sender := &recordingDriftSender{}
			ac.DriftWebhookSender = &webhooks.DriftWebhookSender{Webhooks: []webhooks.DriftSender{sender}}
			When(builder.BuildPlanCommands(Any[*command.Context](), Any[*events.CommentCommand]())).
				Then(func(args []Param) ReturnValues {
					ctx := args[0].(*command.Context)
					Equals(t, tt.includeJSON, ctx.IncludePlanJSON)
					commands := []command.ProjectContext{{
						CommandName:     command.Plan,
						ProjectName:     "app",
						RepoRelDir:      "app",
						Workspace:       events.DefaultWorkspace,
						IncludePlanJSON: ctx.IncludePlanJSON,
					}}
					if tt.policyFailed {
						policy := commands[0]
						policy.CommandName = command.PolicyCheck
						policy.IncludePlanJSON = false
						commands = append(commands, policy)
					}
					return ReturnValues{commands, nil}
				})
			planOutput := "Plan: 1 to add, 0 to change, 0 to destroy."
			if tt.noChanges {
				planOutput = "No changes. Your infrastructure matches the configuration."
			}
			plan := &models.PlanSuccess{TerraformOutput: planOutput, PlanJSON: json.RawMessage(planJSON)}
			if tt.captureFailed {
				plan.PlanJSON = nil
				plan.PlanJSONError = jsonError
			}
			output := command.ProjectCommandOutput{PlanSuccess: plan}
			if tt.planFailed {
				output.Error = errors.New("plan failed")
			}
			When(runner.Plan(Any[command.ProjectContext]())).ThenReturn(output)
			When(runner.PolicyCheck(Any[command.ProjectContext]())).
				ThenReturn(command.ProjectCommandOutput{Error: errors.New("policy failed")})
			body, err := json.Marshal(models.DriftDetectionRequest{
				Repository:        "Repo",
				Ref:               "main",
				Type:              "Gitlab",
				Projects:          []string{"app"},
				IncludePlanOutput: tt.includeOutput,
				IncludePlanJSON:   tt.includeJSON,
			})
			Ok(t, err)
			req := httptest.NewRequest("POST", "/api/drift/detect", bytes.NewReader(body))
			req.Header.Set(atlantisTokenHeader, atlantisToken)
			w := httptest.NewRecorder()

			ac.DetectDrift(w, req)

			wantStatus := http.StatusOK
			wantErrors, wantWebhooks := 0, 1
			if tt.planFailed || tt.policyFailed {
				wantStatus, wantErrors, wantWebhooks = http.StatusMultiStatus, 1, 0
			}
			Equals(t, wantStatus, w.Code)
			var envelope struct {
				Success bool                                `json:"success"`
				Data    controllers.DriftDetectionResultAPI `json:"data"`
			}
			Ok(t, json.Unmarshal(w.Body.Bytes(), &envelope))
			Assert(t, envelope.Success, "expected success response")
			result := envelope.Data
			Equals(t, 1, len(result.Projects))
			Equals(t, wantErrors, result.Summary.ProjectsWithErrors)
			Equals(t, wantWebhooks, sender.calls)
			project := result.Projects[0]
			wantJSON := tt.includeJSON && !tt.captureFailed && !tt.planFailed
			wantJSONError := tt.includeJSON && tt.captureFailed && !tt.planFailed
			Equals(t, wantJSON, strings.Contains(w.Body.String(), `"plan_json":`))
			Equals(t, wantJSONError, strings.Contains(w.Body.String(), `"plan_json_error":`))
			Equals(t, tt.includeOutput && !tt.planFailed, strings.Contains(w.Body.String(), `"plan_output":`))
			Equals(t, !tt.planFailed && !tt.noChanges, project.HasDrift)
			if wantJSON {
				Equals(t, planJSON, string(project.PlanJSON))
			}
			if wantJSONError {
				Equals(t, jsonError, project.PlanJSONError)
				Equals(t, "", project.Error)
				Equals(t, 1, project.Drift.ToAdd)
			}
			stored, err := storage.Get("gitlab.com/Repo", drift.GetOptions{})
			Ok(t, err)
			Equals(t, 1, len(stored))
			Equals(t, 0, len(stored[0].PlanJSON))
			Equals(t, "", stored[0].PlanJSONError)
			webhookJSON, err := json.Marshal(sender.results)
			Ok(t, err)
			Assert(t, !strings.Contains(string(webhookJSON), "plan-json-secret"), "JSON must not enter webhooks")
			Assert(t, !strings.Contains(string(webhookJSON), jsonError), "capture errors must not enter webhooks")
		})
	}
}
