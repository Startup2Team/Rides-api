package tickets

import "time"

type Ticket struct {
	ID           string          `json:"id"`
	Subject      string          `json:"subject"`
	Type         string          `json:"type"`
	Priority     string          `json:"priority"`
	Status       string          `json:"status"`
	FromUserID   *string         `json:"from_user_id"`
	FromName     *string         `json:"from_name"`
	FromPhone    *string         `json:"from_phone"`
	FromRole     *string         `json:"from_role"`
	RideID       *string         `json:"ride_id"`
	AssignedTo   *string         `json:"assigned_to"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Messages     []TicketMessage `json:"messages,omitempty"`
}

type TicketMessage struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	FromRole  string    `json:"from_role"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type ListFilter struct {
	Status   string
	Priority string
	Type     string
	Search   string
	Limit    int
	Offset   int
}
