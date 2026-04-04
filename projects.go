package main

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func registerProjectHooks(app *pocketbase.PocketBase) {
	app.OnRecordCreateRequest("projects").BindFunc(func(e *core.RecordRequestEvent) error {
		e.Record.Set("owner", e.Auth.Id)

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
