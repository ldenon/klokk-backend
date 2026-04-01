package main

import (
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func registerProjectHooks(app *pocketbase.PocketBase) {
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

		return e.Next()
	})
}
