package monetization

import "time"

type Partner struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	LogoURL      *string   `json:"logoUrl"`
	ContactName  string    `json:"contactName"`
	ContactEmail string    `json:"contactEmail"`
	ContactPhone string    `json:"contactPhone"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Advert struct {
	ID        string     `json:"id"`
	PartnerID string     `json:"partnerId"`
	ImageURL  *string    `json:"imageUrl"`
	Headline  string     `json:"headline"`
	CtaLabel  string     `json:"ctaLabel"`
	CtaLink   string     `json:"ctaLink"`
	Active    bool       `json:"active"`
	StartDate *time.Time `json:"startDate"`
	EndDate   *time.Time `json:"endDate"`
	Priority  int        `json:"priority"`
	CreatedAt time.Time  `json:"createdAt"`
}

type CreatePartnerInput struct {
	Name         string  `json:"name"`
	LogoURL      *string `json:"logoUrl"`
	ContactName  string  `json:"contactName"`
	ContactEmail string  `json:"contactEmail"`
	ContactPhone string  `json:"contactPhone"`
	Status       string  `json:"status"`
}

type CreateAdvertInput struct {
	PartnerID string     `json:"partnerId"`
	ImageURL  *string    `json:"imageUrl"`
	Headline  string     `json:"headline"`
	CtaLabel  string     `json:"ctaLabel"`
	CtaLink   string     `json:"ctaLink"`
	Active    bool       `json:"active"`
	StartDate *time.Time `json:"startDate"`
	EndDate   *time.Time `json:"endDate"`
	Priority  int        `json:"priority"`
}

type UpdatePartnerInput struct {
	Name         *string `json:"name"`
	LogoURL      *string `json:"logoUrl"`
	ContactName  *string `json:"contactName"`
	ContactEmail *string `json:"contactEmail"`
	ContactPhone *string `json:"contactPhone"`
	Status       *string `json:"status"`
}

type UpdateAdvertInput struct {
	PartnerID *string    `json:"partnerId"`
	ImageURL  *string    `json:"imageUrl"`
	Headline  *string    `json:"headline"`
	CtaLabel  *string    `json:"ctaLabel"`
	CtaLink   *string    `json:"ctaLink"`
	Active    *bool      `json:"active"`
	StartDate *time.Time `json:"startDate"`
	EndDate   *time.Time `json:"endDate"`
	Priority  *int       `json:"priority"`
}
