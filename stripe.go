package main

import (
	"io"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/webhook"
)

func registerStripeRoutes(app *pocketbase.PocketBase) {

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {

		se.Router.POST("/api/stripe", handleStripeWebhook)
		se.Router.POST("/api/create-checkout-session", handleCreateCheckoutSession)

		return se.Next()
	})
}

func handleStripeWebhook(e *core.RequestEvent) error {

	payload, err := io.ReadAll(e.Request.Body)
	if err != nil {
		return e.BadRequestError("Could not parse request body", nil)
	}

	signatureHeader := e.Request.Header.Get("Stripe-Signature")
	e.App.Logger().Info(signatureHeader)

	event, err := webhook.ConstructEvent(payload, signatureHeader, "whsec_c7c285f434faff6a503133f55dabf7ba43b29e73a13f9d3e7e306c85d3daafee")
	if err != nil {
		return e.BadRequestError("Webhook verification failed", nil)
	}

	switch event.Type {
	case "checkout.session.completed":
		e.App.Logger().Info(string(payload))
	}

	return e.Next()
}

func handleCreateCheckoutSession(e *core.RequestEvent) error {
	info, err := e.RequestInfo()
	if err != nil {
		return e.InternalServerError("An error occured", nil)
	}

	// Check if user is auth
	if info.Auth == nil {
		return e.UnauthorizedError("User not authenticated", nil)
	}

	domain := "http://localhost:3000/"

	params := &stripe.CheckoutSessionParams{
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				// Provide the exact Price ID (for example, price_1234) of the product you want to sell
				Price:    stripe.String("price_1THWCpHLsinIbBUlvwL155WA"),
				Quantity: stripe.Int64(1),
			},
		},
		Mode:       stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL: stripe.String(domain + "?success=true"),
		Metadata: map[string]string{
			"userId": info.Auth.Id,
		},
	}

	s, err := session.New(params)

	if err != nil {
		log.Printf("session.New: %v", err)
		return e.InternalServerError("Failed to create checkout session", err)
	}

	return e.JSON(http.StatusOK, map[string]string{
		"url": s.URL,
	})
}
