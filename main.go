package main

import (
	_ "backend/pb_migrations"
	"log"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
)

func main() {
	app := pocketbase.New()

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: false,
	})

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// registers new "GET /hello" route
		se.Router.GET("/hello", func(re *core.RequestEvent) error {
			return re.String(200, "Hello world!")
		})

		return se.Next()
	})

	// Checks if user has no other active session before create a new one
	app.OnRecordCreateRequest("sessions").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.HasSuperuserAuth() {
			return e.Next()
		}

		count, err := e.App.CountRecords("sessions",
			dbx.And(
				dbx.Not(dbx.NewExp("status = 'completed'")),
				dbx.HashExp{"owner": e.Auth.Id},
			))
		if err != nil {
			return e.InternalServerError("Internal server error.", nil)
		}

		if count > 0 {
			return e.BadRequestError("A session is opened", nil)
		}

		return e.Next()
	})

	// Create a start event after session creation
	app.OnRecordAfterCreateSuccess("sessions").BindFunc(func(e *core.RecordEvent) error {

		collection, err := app.FindCollectionByNameOrId("session_events")
		if err != nil {
			return err
		}
		record := core.NewRecord(collection)

		record.Set("sessionId", e.Record.Id)
		record.Set("action", "start")
		record.Set("occuredAt", e.Record.GetDateTime("lastStartTime"))

		err = app.Save(record)
		if err != nil {
			return err
		}

		return e.Next()
	})

	// Verify if the created event is valid
	app.OnRecordCreateRequest("session_events").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.HasSuperuserAuth() {
			return e.Next()
		}

		sessionId := e.Record.GetString("sessionId")
		events, err := e.App.FindRecordsByFilter(
			"session_events",
			"sessionId = {:sessionId}",
			"-occuredAt",
			1,
			0,
			dbx.Params{"sessionId": sessionId},
		)

		if err != nil {
			return e.InternalServerError("Internal server error.", err)
		}

		if len(events) == 0 {
			return e.Next()
		}

		lastAction := events[0].GetString("action")
		action := e.Record.GetString("action")
		if action == lastAction || lastAction == "stop" {
			return e.BadRequestError("Cannot perform this action: ", action)
		}

		return e.Next()
	})

	// Update session when an event is created
	app.OnRecordAfterCreateSuccess("session_events").BindFunc(func(e *core.RecordEvent) error {
		session, err := app.FindRecordById("sessions", e.Record.GetString("sessionId"))
		if err != nil {
			return err
		}

		eventOccuredAt := e.Record.GetDateTime("occuredAt")
		lastStartTime := session.GetDateTime("lastStartTime")

		switch e.Record.GetString("action") {
		case "start":
			session.Set("status", "active")
			session.Set("lastStartTime", eventOccuredAt)
		case "pause":
			session.Set("status", "paused")
			// Update total duration
			duration := eventOccuredAt.Sub(lastStartTime).Milliseconds()
			newTotal := int64(session.GetInt("totalTime")) + duration
			session.Set("totalTime", newTotal)
		case "stop":
			session.Set("status", "completed")
			// Update total duration
			duration := eventOccuredAt.Sub(lastStartTime).Milliseconds()
			newTotal := int64(session.GetInt("totalTime")) + duration
			session.Set("totalTime", newTotal)
		}

		e.App.Save(session)

		return e.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
