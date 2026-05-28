package inbox

import "time"

type Message struct {
	ID        string     `json:"id"`
	FromName  string     `json:"from_name"`
	FromEmail string     `json:"from_email"`
	Category  string     `json:"category"`
	Status    string     `json:"status"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	ReplyBody *string    `json:"reply_body,omitempty"`
	RepliedAt *time.Time `json:"replied_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type ListFilter struct {
	Status   string
	Category string
	Search   string
	Limit    int
	Offset   int
}
