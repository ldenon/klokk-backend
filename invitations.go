package main

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

type inviteCreatePayload struct {
	Project   string `json:"project"`
	ProjectId string `json:"projectId"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

type inviteActionPayload struct {
	Action string `json:"action"`
	Status string `json:"status"`
}

func registerInvitationRoutes(app *pocketbase.PocketBase) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.POST("/api/invitations", handleCreateInvitation).Bind(apis.RequireAuth())
		se.Router.GET("/api/invitations/{token}", handleGetInvitation).Bind(apis.RequireAuth())
		se.Router.POST("/api/invitations/{token}", handleInvitationAction)
		return se.Next()
	})
}

func handleCreateInvitation(e *core.RequestEvent) error {
	payload := inviteCreatePayload{}
	if err := json.NewDecoder(e.Request.Body).Decode(&payload); err != nil {
		return e.BadRequestError("Invalid request body", nil)
	}

	projectId := strings.TrimSpace(payload.ProjectId)
	if projectId == "" {
		projectId = strings.TrimSpace(payload.Project)
	}

	email := strings.ToLower(strings.TrimSpace(payload.Email))
	role := strings.TrimSpace(payload.Role)
	if role == "" {
		role = "default"
	}

	if projectId == "" || email == "" {
		return e.BadRequestError("Project and email are required", nil)
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return e.BadRequestError("Invalid email address", nil)
	}

	project, err := e.App.FindRecordById("projects", projectId)
	if err != nil {
		return e.BadRequestError("Project not found", nil)
	}
	if project.GetString("owner") != e.Auth.Id {
		return e.ForbiddenError("Not allowed to invite for this project", nil)
	}

	now := time.Now().UTC()
	existing, err := e.App.FindRecordsByFilter(
		"project_invitations",
		"project = {:project} && email = {:email} && status = 'pending' && expiry > {:now}",
		"-expiry",
		1,
		0,
		dbx.Params{
			"project": projectId,
			"email":   email,
			"now":     now,
		},
	)
	if err != nil {
		return e.InternalServerError("Failed to check invitations", err)
	}
	if len(existing) > 0 {
		return e.JSON(http.StatusOK, buildInvitationInfo(e, existing[0]))
	}

	collection, err := e.App.FindCollectionByNameOrId("project_invitations")
	if err != nil {
		return e.InternalServerError("Could not find invitations collection", err)
	}

	record := core.NewRecord(collection)
	record.Set("project", projectId)
	record.Set("email", email)
	record.Set("role", role)
	record.Set("token", uuid.NewString())
	record.Set("status", "pending")
	record.Set("invitedBy", e.Auth.Id)
	record.Set("expiry", now.Add(24*time.Hour))

	if err := e.App.Save(record); err != nil {
		return e.InternalServerError("Could not create invitation", err)
	}

	return e.JSON(http.StatusOK, buildInvitationInfo(e, record))
}

func handleGetInvitation(e *core.RequestEvent) error {
	token := strings.TrimSpace(e.Request.PathValue("token"))
	if token == "" {
		return e.BadRequestError("Missing invitation token", nil)
	}
	if e.Auth == nil {
		return e.UnauthorizedError("User not authenticated", nil)
	}

	records, err := e.App.FindRecordsByFilter(
		"project_invitations",
		"token = {:token}",
		"",
		1,
		0,
		dbx.Params{"token": token},
	)
	if err != nil {
		return e.InternalServerError("Failed to lookup invitation", err)
	}
	if len(records) == 0 {
		return e.BadRequestError("Invitation not found", nil)
	}

	invitation := records[0]
	authEmail := strings.ToLower(strings.TrimSpace(e.Auth.GetString("email")))
	inviteEmail := strings.ToLower(strings.TrimSpace(invitation.GetString("email")))
	if authEmail == "" {
		return e.UnauthorizedError("User email is missing", nil)
	}
	if authEmail != inviteEmail {
		return e.ForbiddenError("Invitation email does not match", nil)
	}

	expiry := invitation.GetDateTime("expiry")
	if !expiry.IsZero() && expiry.Time().Before(time.Now().UTC()) {
		if invitation.GetString("status") == "pending" {
			invitation.Set("status", "expired")
			if err := e.App.Save(invitation); err != nil {
				return e.InternalServerError("Could not update invitation", err)
			}
		}
		return e.BadRequestError("Invitation expired", nil)
	}

	return e.JSON(http.StatusOK, buildInvitationInfo(e, invitation))
}

func handleInvitationAction(e *core.RequestEvent) error {
	token := strings.TrimSpace(e.Request.PathValue("token"))
	if token == "" {
		return e.BadRequestError("Missing invitation token", nil)
	}
	if e.Auth == nil {
		return e.UnauthorizedError("User not authenticated", nil)
	}

	payload := inviteActionPayload{}
	if err := json.NewDecoder(e.Request.Body).Decode(&payload); err != nil {
		return e.BadRequestError("Invalid request body", nil)
	}

	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(payload.Status))
	}

	nextStatus := ""
	switch action {
	case "accept", "accepted":
		nextStatus = "accepted"
	case "decline", "declined", "reject", "rejected":
		nextStatus = "declined"
	default:
		return e.BadRequestError("Invalid invitation action", nil)
	}

	records, err := e.App.FindRecordsByFilter(
		"project_invitations",
		"token = {:token}",
		"",
		1,
		0,
		dbx.Params{"token": token},
	)
	if err != nil {
		return e.InternalServerError("Failed to lookup invitation", err)
	}
	if len(records) == 0 {
		return e.BadRequestError("Invitation not found", nil)
	}

	invitation := records[0]
	authEmail := strings.ToLower(strings.TrimSpace(e.Auth.GetString("email")))
	inviteEmail := strings.ToLower(strings.TrimSpace(invitation.GetString("email")))
	if authEmail == "" {
		return e.UnauthorizedError("User email is missing", nil)
	}
	if authEmail != inviteEmail {
		return e.ForbiddenError("Invitation email does not match", nil)
	}

	if invitation.GetString("status") != "pending" {
		return e.BadRequestError("Invitation already handled", nil)
	}

	expiry := invitation.GetDateTime("expiry")
	if !expiry.IsZero() && expiry.Time().Before(time.Now().UTC()) {
		invitation.Set("status", "expired")
		if err := e.App.Save(invitation); err != nil {
			return e.InternalServerError("Could not update invitation", err)
		}
		return e.BadRequestError("Invitation expired", nil)
	}

	if nextStatus == "accepted" {
		projectId := invitation.GetString("project")
		if projectId == "" {
			return e.BadRequestError("Project not found", nil)
		}
		project, err := e.App.FindRecordById("projects", projectId)
		if err != nil {
			return e.BadRequestError("Project not found", nil)
		}

		members := project.GetStringSlice("members")
		alreadyMember := false
		for _, member := range members {
			if member == e.Auth.Id {
				alreadyMember = true
				break
			}
		}
		if !alreadyMember {
			members = append(members, e.Auth.Id)
			project.Set("members", members)
			if err := e.App.Save(project); err != nil {
				return e.InternalServerError("Could not update project members", err)
			}
		}
	}

	invitation.Set("status", nextStatus)
	if err := e.App.Save(invitation); err != nil {
		return e.InternalServerError("Could not update invitation", err)
	}

	return e.JSON(http.StatusOK, map[string]any{
		"status":  invitation.GetString("status"),
		"project": invitation.GetString("project"),
		"email":   invitation.GetString("email"),
	})
}

func buildInvitationInfo(e *core.RequestEvent, record *core.Record) map[string]any {
	projectId := record.GetString("project")
	projectName := ""
	if e != nil && projectId != "" {
		if project, err := e.App.FindRecordById("projects", projectId); err == nil {
			projectName = strings.TrimSpace(project.GetString("name"))
		}
	}

	inviterId := record.GetString("invitedBy")
	inviterName := ""
	if e != nil && inviterId != "" {
		if inviter, err := e.App.FindRecordById("users", inviterId); err == nil {
			inviterName = strings.TrimSpace(inviter.GetString("name"))
			if inviterName == "" {
				inviterName = strings.TrimSpace(inviter.GetString("email"))
			}
		}
	}
	if inviterName == "" {
		inviterName = "Someone"
	}

	return map[string]any{
		"token":       record.GetString("token"),
		"status":      record.GetString("status"),
		"email":       record.GetString("email"),
		"role":        record.GetString("role"),
		"projectId":   projectId,
		"projectName": projectName,
		"inviterId":   inviterId,
		"inviterName": inviterName,
		"expiry":      record.GetDateTime("expiry"),
	}
}
