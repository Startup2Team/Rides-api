package notification

import (
	"context"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

// firebaseClient wraps the Firebase Admin SDK messaging client.
type firebaseClient struct {
	msg *messaging.Client
}

func newFirebaseClient(serviceAccountPath string) (*firebaseClient, error) {
	app, err := firebase.NewApp(context.Background(), nil,
		option.WithServiceAccountFile(serviceAccountPath),
	)
	if err != nil {
		return nil, fmt.Errorf("fcm: init firebase app: %w", err)
	}

	msg, err := app.Messaging(context.Background())
	if err != nil {
		return nil, fmt.Errorf("fcm: get messaging client: %w", err)
	}

	return &firebaseClient{msg: msg}, nil
}

func (f *firebaseClient) Send(ctx context.Context, token, title, body string, data map[string]string) error {
	msg := &messaging.Message{
		Token: token,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-priority": "10",
			},
		},
	}

	_, err := f.msg.Send(ctx, msg)
	if err != nil {
		// Classify dead tokens so the caller can prune them. Wrap with %w so
		// errors.Is(err, ErrTokenUnregistered) works upstream.
		if messaging.IsRegistrationTokenNotRegistered(err) || messaging.IsUnregistered(err) {
			return fmt.Errorf("%w: %v", ErrTokenUnregistered, err)
		}
		return fmt.Errorf("fcm: send: %w", err)
	}
	return nil
}
