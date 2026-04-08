package main

import (
	_ "backend/pb_migrations"
	"log"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
)

func main() {
	app := pocketbase.New()

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate: false,
	})

	registerSessionRoutes(app)
	registerSessionHooks(app)
	registerProjectHooks(app)
	registerInvitationRoutes(app)
	// Pricing disabled: Stripe routes are temporarily turned off.
	// registerStripeRoutes(app)

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
