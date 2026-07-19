package monetization

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ── Partners ──────────────────────────────────────────────────────────────

func (r *Repository) ListPartners(ctx context.Context) ([]*Partner, error) {
	query := `SELECT id, name, logo_url, contact_name, contact_email, contact_phone, status, created_at 
	          FROM partners ORDER BY created_at DESC`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var partners []*Partner
	for rows.Next() {
		var p Partner
		var idUUID uuid.UUID
		err = rows.Scan(&idUUID, &p.Name, &p.LogoURL, &p.ContactName, &p.ContactEmail, &p.ContactPhone, &p.Status, &p.CreatedAt)
		if err != nil {
			return nil, err
		}
		p.ID = idUUID.String()
		partners = append(partners, &p)
	}
	return partners, nil
}

func (r *Repository) GetPartnerByID(ctx context.Context, id string) (*Partner, error) {
	idUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, name, logo_url, contact_name, contact_email, contact_phone, status, created_at 
	          FROM partners WHERE id = $1`
	var p Partner
	err = r.db.QueryRow(ctx, query, idUUID).Scan(&idUUID, &p.Name, &p.LogoURL, &p.ContactName, &p.ContactEmail, &p.ContactPhone, &p.Status, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	p.ID = idUUID.String()
	return &p, nil
}

func (r *Repository) CreatePartner(ctx context.Context, input CreatePartnerInput) (*Partner, error) {
	id := uuid.New()
	query := `INSERT INTO partners (id, name, logo_url, contact_name, contact_email, contact_phone, status) 
	          VALUES ($1, $2, $3, $4, $5, $6, $7) 
	          RETURNING id, name, logo_url, contact_name, contact_email, contact_phone, status, created_at`
	var p Partner
	var idUUID uuid.UUID
	err := r.db.QueryRow(ctx, query, id, input.Name, input.LogoURL, input.ContactName, input.ContactEmail, input.ContactPhone, input.Status).
		Scan(&idUUID, &p.Name, &p.LogoURL, &p.ContactName, &p.ContactEmail, &p.ContactPhone, &p.Status, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	p.ID = idUUID.String()
	return &p, nil
}

func (r *Repository) UpdatePartner(ctx context.Context, id string, input UpdatePartnerInput) (*Partner, error) {
	idUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	existing, err := r.GetPartnerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}

	name := existing.Name
	if input.Name != nil {
		name = *input.Name
	}
	logoURL := existing.LogoURL
	if input.LogoURL != nil {
		logoURL = input.LogoURL
	}
	contactName := existing.ContactName
	if input.ContactName != nil {
		contactName = *input.ContactName
	}
	contactEmail := existing.ContactEmail
	if input.ContactEmail != nil {
		contactEmail = *input.ContactEmail
	}
	contactPhone := existing.ContactPhone
	if input.ContactPhone != nil {
		contactPhone = *input.ContactPhone
	}
	status := existing.Status
	if input.Status != nil {
		status = *input.Status
	}

	query := `UPDATE partners 
	          SET name = $1, logo_url = $2, contact_name = $3, contact_email = $4, contact_phone = $5, status = $6
	          WHERE id = $7 
	          RETURNING id, name, logo_url, contact_name, contact_email, contact_phone, status, created_at`
	var p Partner
	var retUUID uuid.UUID
	err = r.db.QueryRow(ctx, query, name, logoURL, contactName, contactEmail, contactPhone, status, idUUID).
		Scan(&retUUID, &p.Name, &p.LogoURL, &p.ContactName, &p.ContactEmail, &p.ContactPhone, &p.Status, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	p.ID = retUUID.String()
	return &p, nil
}

func (r *Repository) DeletePartner(ctx context.Context, id string) error {
	idUUID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	query := `DELETE FROM partners WHERE id = $1`
	_, err = r.db.Exec(ctx, query, idUUID)
	return err
}

// ── Adverts ───────────────────────────────────────────────────────────────

func (r *Repository) ListAdverts(ctx context.Context) ([]*Advert, error) {
	query := `SELECT id, partner_id, image_url, headline, cta_label, cta_link, active, start_date, end_date, priority, created_at 
	          FROM adverts ORDER BY priority ASC, created_at DESC`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var adverts []*Advert
	for rows.Next() {
		var a Advert
		var idUUID, partnerUUID uuid.UUID
		err = rows.Scan(&idUUID, &partnerUUID, &a.ImageURL, &a.Headline, &a.CtaLabel, &a.CtaLink, &a.Active, &a.StartDate, &a.EndDate, &a.Priority, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		a.ID = idUUID.String()
		a.PartnerID = partnerUUID.String()
		adverts = append(adverts, &a)
	}
	return adverts, nil
}

func (r *Repository) GetAdvertByID(ctx context.Context, id string) (*Advert, error) {
	idUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, partner_id, image_url, headline, cta_label, cta_link, active, start_date, end_date, priority, created_at 
	          FROM adverts WHERE id = $1`
	var a Advert
	var partnerUUID uuid.UUID
	err = r.db.QueryRow(ctx, query, idUUID).Scan(&idUUID, &partnerUUID, &a.ImageURL, &a.Headline, &a.CtaLabel, &a.CtaLink, &a.Active, &a.StartDate, &a.EndDate, &a.Priority, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	a.ID = idUUID.String()
	a.PartnerID = partnerUUID.String()
	return &a, nil
}

func (r *Repository) CreateAdvert(ctx context.Context, input CreateAdvertInput) (*Advert, error) {
	id := uuid.New()
	partnerUUID, err := uuid.Parse(input.PartnerID)
	if err != nil {
		return nil, err
	}
	query := `INSERT INTO adverts (id, partner_id, image_url, headline, cta_label, cta_link, active, start_date, end_date, priority) 
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) 
	          RETURNING id, partner_id, image_url, headline, cta_label, cta_link, active, start_date, end_date, priority, created_at`
	var a Advert
	var idUUID, retPartnerUUID uuid.UUID
	err = r.db.QueryRow(ctx, query, id, partnerUUID, input.ImageURL, input.Headline, input.CtaLabel, input.CtaLink, input.Active, input.StartDate, input.EndDate, input.Priority).
		Scan(&idUUID, &retPartnerUUID, &a.ImageURL, &a.Headline, &a.CtaLabel, &a.CtaLink, &a.Active, &a.StartDate, &a.EndDate, &a.Priority, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	a.ID = idUUID.String()
	a.PartnerID = retPartnerUUID.String()
	return &a, nil
}

func (r *Repository) UpdateAdvert(ctx context.Context, id string, input UpdateAdvertInput) (*Advert, error) {
	idUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	existing, err := r.GetAdvertByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}

	partnerID := existing.PartnerID
	if input.PartnerID != nil {
		partnerID = *input.PartnerID
	}
	partnerUUID, err := uuid.Parse(partnerID)
	if err != nil {
		return nil, err
	}

	imageURL := existing.ImageURL
	if input.ImageURL != nil {
		imageURL = input.ImageURL
	}
	headline := existing.Headline
	if input.Headline != nil {
		headline = *input.Headline
	}
	ctaLabel := existing.CtaLabel
	if input.CtaLabel != nil {
		ctaLabel = *input.CtaLabel
	}
	ctaLink := existing.CtaLink
	if input.CtaLink != nil {
		ctaLink = *input.CtaLink
	}
	active := existing.Active
	if input.Active != nil {
		active = *input.Active
	}
	startDate := existing.StartDate
	if input.StartDate != nil {
		startDate = input.StartDate
	}
	endDate := existing.EndDate
	if input.EndDate != nil {
		endDate = input.EndDate
	}
	priority := existing.Priority
	if input.Priority != nil {
		priority = *input.Priority
	}

	query := `UPDATE adverts 
	          SET partner_id = $1, image_url = $2, headline = $3, cta_label = $4, cta_link = $5, active = $6, start_date = $7, end_date = $8, priority = $9
	          WHERE id = $10 
	          RETURNING id, partner_id, image_url, headline, cta_label, cta_link, active, start_date, end_date, priority, created_at`
	var a Advert
	var retUUID, retPartnerUUID uuid.UUID
	err = r.db.QueryRow(ctx, query, partnerUUID, imageURL, headline, ctaLabel, ctaLink, active, startDate, endDate, priority, idUUID).
		Scan(&retUUID, &retPartnerUUID, &a.ImageURL, &a.Headline, &a.CtaLabel, &a.CtaLink, &a.Active, &a.StartDate, &a.EndDate, &a.Priority, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	a.ID = retUUID.String()
	a.PartnerID = retPartnerUUID.String()
	return &a, nil
}

func (r *Repository) DeleteAdvert(ctx context.Context, id string) error {
	idUUID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	query := `DELETE FROM adverts WHERE id = $1`
	_, err = r.db.Exec(ctx, query, idUUID)
	return err
}

// ── Mobile query ──────────────────────────────────────────────────────────

func (r *Repository) ListActiveAdverts(ctx context.Context) ([]*Advert, error) {
	query := `SELECT id, partner_id, image_url, headline, cta_label, cta_link, active, start_date, end_date, priority, created_at 
	          FROM adverts 
	          WHERE active = true 
	            AND (start_date IS NULL OR start_date <= NOW())
	            AND (end_date IS NULL OR end_date >= NOW())
	          ORDER BY priority ASC, created_at DESC`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var adverts []*Advert
	for rows.Next() {
		var a Advert
		var idUUID, partnerUUID uuid.UUID
		err = rows.Scan(&idUUID, &partnerUUID, &a.ImageURL, &a.Headline, &a.CtaLabel, &a.CtaLink, &a.Active, &a.StartDate, &a.EndDate, &a.Priority, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		a.ID = idUUID.String()
		a.PartnerID = partnerUUID.String()
		adverts = append(adverts, &a)
	}
	return adverts, nil
}
