package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/customer"
	"github.com/stripe/stripe-go/v85/webhook"
)

var (
	WHSEC                    string
	STRIPE_SUCCESS_URL       string
	STRIPE_LIFETIME_PRICE_ID string
)

func registerStripeRoutes(app *pocketbase.PocketBase) {

	err := godotenv.Load()
	if err != nil {
		app.Logger().Error(err.Error())
	}

	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")
	WHSEC = os.Getenv("WHSEC")
	STRIPE_SUCCESS_URL = os.Getenv("STRIPE_SUCCESS_URL")
	STRIPE_LIFETIME_PRICE_ID = os.Getenv("STRIPE_LIFETIME_PRICE_ID")

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
	if signatureHeader == "" {
		return e.BadRequestError("Missing Signature header", nil)
	}

	event, err := webhook.ConstructEvent(payload, signatureHeader, WHSEC)
	if err != nil {
		return e.BadRequestError("Webhook verification failed", nil)
	}

	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		var checkoutSession stripe.CheckoutSession
		err = json.Unmarshal(event.Data.Raw, &checkoutSession)
		if err != nil {
			e.App.Logger().Error("failed to unmarshall the stripe checkout session event", "error", err)
			return e.BadRequestError("Could not parse session", nil)
		}

		if checkoutSession.Mode != stripe.CheckoutSessionModePayment {
			return e.Next()
		}

		markPaid := checkoutSession.PaymentStatus == "paid"
		if event.Type == "checkout.session.async_payment_succeeded" {
			markPaid = true
		}

		if err := processLifetimeCheckoutSession(e, checkoutSession, markPaid); err != nil {
			return err
		}
	}

	return e.Next()
}

func processLifetimeCheckoutSession(e *core.RequestEvent, checkoutSession stripe.CheckoutSession, markPaid bool) error {
	if STRIPE_LIFETIME_PRICE_ID == "" {
		return e.InternalServerError("Missing lifetime price id", nil)
	}
	if checkoutSession.Customer == nil {
		return e.BadRequestError("Could not find customer", nil)
	}

	priceMatch, err := sessionHasLifetimePrice(checkoutSession.ID)
	if err != nil {
		return e.InternalServerError("Could not verify checkout session price", err)
	}
	if !priceMatch {
		return e.BadRequestError("Unexpected price in checkout session", nil)
	}

	customerId := checkoutSession.Customer.ID
	customerRecord, err := e.App.FindFirstRecordByData("customers", "stripeId", customerId)
	if err != nil {
		return e.BadRequestError("Customer not found", nil)
	}

	errs := e.App.ExpandRecord(customerRecord, []string{"user"}, nil)
	if len(errs) > 0 {
		return e.BadRequestError("User not found", nil)
	}

	user := customerRecord.ExpandedOne("user")
	if user == nil {
		return e.BadRequestError("User not found", nil)
	}

	status := "active"
	if !markPaid {
		status = "past_due"
	}

	existing, _ := e.App.FindFirstRecordByData("subscriptions", "user", user.Id)
	if existing != nil && existing.GetString("type") == "lifetime" {
		existing.Set("status", status)
		if markPaid {
			existing.Set("expiresAt", time.Now().AddDate(100, 0, 0))
		}
		if err := e.App.Save(existing); err != nil {
			return e.InternalServerError("Could not update subscription", err)
		}
		return nil
	}

	collection, err := e.App.FindCollectionByNameOrId("subscriptions")
	if err != nil {
		return e.InternalServerError("An error occured", nil)
	}
	record := core.NewRecord(collection)
	record.Set("user", user.Id)
	record.Set("stripePriceId", STRIPE_LIFETIME_PRICE_ID)
	record.Set("type", "lifetime")
	record.Set("status", status)
	record.Set("expiresAt", time.Now().AddDate(100, 0, 0))
	if err := e.App.Save(record); err != nil {
		return e.BadRequestError("Could not create subscription", nil)
	}

	return nil
}

func sessionHasLifetimePrice(sessionID string) (bool, error) {
	fullSession, err := session.Get(sessionID, &stripe.CheckoutSessionParams{
		Expand: []*string{stripe.String("line_items")},
	})
	if err != nil {
		return false, err
	}
	if fullSession.LineItems == nil {
		return false, nil
	}
	for _, item := range fullSession.LineItems.Data {
		if item.Price != nil && item.Price.ID == STRIPE_LIFETIME_PRICE_ID {
			return true, nil
		}
	}
	return false, nil
}

func handleCreateCheckoutSession(e *core.RequestEvent) error {

	info, err := e.RequestInfo()
	if err != nil {
		return e.InternalServerError("An error occured", nil)
	}

	// User auth ?
	// Check if user is auth
	if info.Auth == nil {
		return e.UnauthorizedError("User not authenticated", nil)
	}

	// Customer exists ?
	customerRecord, err := e.App.FindFirstRecordByData("customers", "user", info.Auth.Id)
	// If not create it
	if err != nil {
		customersCollection, err := e.App.FindCollectionByNameOrId("customers")
		if err != nil {
			return e.InternalServerError("An error occured", nil)
		}
		customerEmail := info.Auth.Email()
		customerParams := &stripe.CustomerParams{
			Email: &customerEmail,
			Metadata: map[string]string{
				"userId": info.Auth.Id,
			},
		}

		stripeCustomer, err := customer.New(customerParams)

		if err != nil {
			e.App.Logger().Error("could not create customer", "error", err)
			return e.InternalServerError("An error occured", nil)
		}

		customerRecord = core.NewRecord(customersCollection)
		customerRecord.Set("user", info.Auth.Id)
		customerRecord.Set("stripeId", stripeCustomer.ID)

		err = e.App.Save(customerRecord)
		if err != nil {
			return e.InternalServerError("Could not create new customer", nil)
		}
	}

	customerId := customerRecord.GetString("stripeId")
	// Create checkout from customerId
	params := &stripe.CheckoutSessionParams{
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				// Provide the exact Price ID (for example, price_1234) of the product you want to sell
				Price:    stripe.String(STRIPE_LIFETIME_PRICE_ID),
				Quantity: stripe.Int64(1),
			},
		},
		Mode:                stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:          stripe.String(STRIPE_SUCCESS_URL + "?success=true"),
		AllowPromotionCodes: stripe.Bool(true),
		Customer:            &customerId,
	}

	s, err := session.New(params)
	if err != nil {
		return e.InternalServerError("Failed to create checkout session", err)
	}

	return e.JSON(http.StatusOK, map[string]string{
		"url": s.URL,
	})
}
