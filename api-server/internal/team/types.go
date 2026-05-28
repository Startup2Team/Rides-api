package team

import "time"

type AdminAccount struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	RoleID       string     `json:"role_id"`
	RoleName     string     `json:"role_name"`
	Status       string     `json:"status"`
	TwoFactor    bool       `json:"two_factor"`
	LastActiveAt *time.Time `json:"last_active_at"`
	InvitedAt    time.Time  `json:"invited_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Role struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
	Permissions interface{} `json:"permissions"`
	IsSystem    bool        `json:"is_system"`
	CreatedAt   time.Time   `json:"created_at"`
}
