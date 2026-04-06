package main

import (
	"errors"
	"net/http"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

type SessionEvent struct {
	Id        string         `db:"id" json:"id"`
	Session   string         `db:"session" json:"session"`
	Action    string         `db:"action" json:"action"`
	OccuredAt types.DateTime `db:"occuredAt" json:"occuredAt"`
}

type SessionInterval struct {
	Id        string         `db:"id" json:"id"`
	Session   string         `db:"session" json:"session"`
	StartTime types.DateTime `db:"startTime" json:"startTime"`
	EndTime   types.DateTime `db:"endTime" json:"endTime"`
}

func closeLatestSessionInterval(app core.App, sessionId string, endTime types.DateTime) (int64, error) {
	intervals, err := app.FindRecordsByFilter(
		"session_intervals",
		"session = {:session}",
		"-startTime",
		1,
		0,
		dbx.Params{"session": sessionId},
	)
	if err != nil {
		return 0, err
	}
	if len(intervals) == 0 {
		return 0, errors.New("no session interval found")
	}

	interval := intervals[0]
	if !interval.GetDateTime("endTime").IsZero() {
		return 0, errors.New("session interval already closed")
	}

	startTime := interval.GetDateTime("startTime")
	if startTime.IsZero() {
		return 0, errors.New("session interval missing start time")
	}

	interval.Set("endTime", endTime)
	if err := app.Save(interval); err != nil {
		return 0, err
	}

	return endTime.Time().UTC().Sub(startTime.Time().UTC()).Milliseconds(), nil
}

func registerSessionRoutes(app *pocketbase.PocketBase) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Recalculate a session's time
		se.Router.POST("/api/sessions/{id}/refresh", func(re *core.RequestEvent) error {
			id := re.Request.PathValue("id")

			session, err := re.App.FindRecordById("sessions", id)
			if err != nil {
				return re.BadRequestError("No session found.", nil)
			}

			intervals := []SessionInterval{}
			err = re.App.DB().NewQuery("SELECT id, session, startTime, endTime FROM session_intervals WHERE session = {:session} ORDER BY startTime ASC").Bind(dbx.Params{
				"session": session.Id,
			}).All(&intervals)
			if err != nil {
				return re.InternalServerError("An error occured.", err)
			}

			var totalDuration int64 = 0

			for _, interval := range intervals {
				if interval.StartTime.IsZero() || interval.EndTime.IsZero() {
					continue
				}
				intervalDuration := interval.EndTime.Time().Sub(interval.StartTime.Time()).Milliseconds()
				totalDuration += intervalDuration
			}

			session.Set("duration", totalDuration)
			err = re.App.Save(session)
			if err != nil {
				return re.InternalServerError("Failed to update session.", err)
			}

			return re.JSON(http.StatusOK, map[string]interface{}{
				"duration": totalDuration,
			})
		}).Bind(apis.RequireAuth())

		return se.Next()
	})
}

func registerSessionHooks(app *pocketbase.PocketBase) {
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
		startAt := e.Record.GetDateTime("activeSince")
		if startAt.IsZero() {
			startAt = types.NowDateTime()
			e.Record.Set("activeSince", startAt)
			if err := e.App.Save(e.Record); err != nil {
				return err
			}
		}

		record.Set("session", e.Record.Id)
		record.Set("action", "start")
		record.Set("occuredAt", startAt)

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

		session := e.Record.GetString("session")
		events, err := e.App.FindRecordsByFilter(
			"session_events",
			"session = {:session}",
			"-occuredAt",
			1,
			0,
			dbx.Params{"session": session},
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
		session, err := app.FindRecordById("sessions", e.Record.GetString("session"))
		if err != nil {
			return err
		}

		eventOccuredAt := e.Record.GetDateTime("occuredAt")

		switch e.Record.GetString("action") {
		case "start":
			session.Set("status", "active")
			session.Set("activeSince", eventOccuredAt)

			intervalCollection, err := e.App.FindCollectionByNameOrId("session_intervals")
			if err != nil {
				return err
			}
			interval := core.NewRecord(intervalCollection)
			interval.Set("session", session.Id)
			interval.Set("startTime", eventOccuredAt)

			if err := e.App.Save(interval); err != nil {
				return err
			}
		case "pause":
			session.Set("status", "paused")

			duration, err := closeLatestSessionInterval(e.App, session.Id, eventOccuredAt)
			if err != nil {
				return err
			}
			session.Set("duration", int64(session.GetInt("duration"))+duration)
		case "stop":
			if session.GetString("status") == "active" {
				duration, err := closeLatestSessionInterval(e.App, session.Id, eventOccuredAt)
				if err != nil {
					return err
				}
				session.Set("duration", int64(session.GetInt("duration"))+duration)
			}
			session.Set("status", "completed")
		}

		if err := e.App.Save(session); err != nil {
			return err
		}

		return e.Next()
	})
}
