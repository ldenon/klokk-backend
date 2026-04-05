package main

import (
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func registerProjectHooks(app *pocketbase.PocketBase) {
	app.OnRecordCreateRequest("projects").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.HasSuperuserAuth() {
			return e.Next()
		}
		if e.Auth == nil {
			return e.UnauthorizedError("User not authenticated", nil)
		}

		hasLifetime, err := userHasActiveLifetime(e)
		if err != nil {
			return e.InternalServerError("Failed to check subscription", err)
		}
		if !hasLifetime {
			count, err := e.App.CountRecords("projects", dbx.HashExp{"owner": e.Auth.Id})
			if err != nil {
				return e.InternalServerError("Failed to count projects", err)
			}
			if count >= 3 {
				return e.BadRequestError("Project limit reached", nil)
			}
		}

		e.Record.Set("owner", e.Auth.Id)

		return e.Next()
	})

	app.OnRecordCreateRequest("project_tags").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.HasSuperuserAuth() {
			return e.Next()
		}
		if e.Auth == nil {
			return e.UnauthorizedError("User not authenticated", nil)
		}

		hasLifetime, err := userHasActiveLifetime(e)
		if err != nil {
			return e.InternalServerError("Failed to check subscription", err)
		}
		if hasLifetime {
			return e.Next()
		}

		projectId := e.Record.GetString("project")
		if projectId == "" {
			return e.BadRequestError("Project is required", nil)
		}

		count, err := e.App.CountRecords("project_tags", dbx.HashExp{"project": projectId})
		if err != nil {
			return e.InternalServerError("Failed to count project tags", err)
		}
		if count >= 10 {
			return e.BadRequestError("Tag limit reached", nil)
		}

		return e.Next()
	})

	// On user signup
	app.OnRecordAfterCreateSuccess("users").BindFunc(func(e *core.RecordEvent) error {
		collection, err := app.FindCollectionByNameOrId("projects")
		if err != nil {
			return err
		}

		project := core.NewRecord(collection)
		project.Set("name", "Personal")
		project.Set("owner", e.Record.Id)

		if err := app.Save(project); err != nil {
			return err
		}

		e.Record.Set("currentProject", project.Id)
		if err := app.Save(e.Record); err != nil {
			return err
		}

		tasksCollection, err := app.FindCollectionByNameOrId("tasks")
		if err != nil {
			return err
		}

		taskNames := []string{
			"Welcome to Klokk! 👋",
			"Start a session instantly from a task 📋",
			"Tag sessions to organize projects 🏌🏻",
		}

		for _, name := range taskNames {
			task := core.NewRecord(tasksCollection)
			task.Set("user", e.Record.Id)
			task.Set("name", name)
			task.Set("project", project.Id)
			task.Set("done", false)
			if err := app.Save(task); err != nil {
				return err
			}
		}

		return e.Next()
	})
}

func userHasActiveLifetime(e *core.RecordRequestEvent) (bool, error) {
	if e.Auth == nil {
		return false, nil
	}
	filters := "user = {:user} && type = 'lifetime' && status = 'active' && expiresAt > {:now}"
	records, err := e.App.FindRecordsByFilter(
		"subscriptions",
		filters,
		"-expiresAt",
		1,
		0,
		dbx.Params{
			"user": e.Auth.Id,
			"now":  time.Now().UTC(),
		},
	)
	if err != nil {
		return false, err
	}
	return len(records) > 0, nil
}
