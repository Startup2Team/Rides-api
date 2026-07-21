//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/monetization"
)

func TestMonetization_PartnerAndActiveAdvert(t *testing.T) {
	ctx := context.Background()
	repo := monetization.NewRepository(pool)

	p, err := repo.CreatePartner(ctx, monetization.CreatePartnerInput{
		Name:         "Acme " + uniqueKey("p"),
		ContactName:  "Jane",
		ContactEmail: "jane@acme.test",
		ContactPhone: "+250780000000",
		Status:       "active",
	})
	require.NoError(t, err)
	require.NotEmpty(t, p.ID)

	got, err := repo.GetPartnerByID(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, p.ID, got.ID)

	headline := "Headline " + uniqueKey("ad")
	a, err := repo.CreateAdvert(ctx, monetization.CreateAdvertInput{
		PartnerID: p.ID,
		Headline:  headline,
		CtaLabel:  "Book now",
		CtaLink:   "https://acme.test",
		Active:    true,
		Priority:  1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, a.ID)

	// The active advert must be delivered by the public endpoint's query.
	active, err := repo.ListActiveAdverts(ctx)
	require.NoError(t, err)
	found := false
	for _, ad := range active {
		if ad.ID == a.ID {
			found = true
			require.Equal(t, headline, ad.Headline)
		}
	}
	require.True(t, found, "an active advert must appear in ListActiveAdverts")
}

func TestMonetization_DeletePartnerCascadesAdverts(t *testing.T) {
	ctx := context.Background()
	repo := monetization.NewRepository(pool)

	p, err := repo.CreatePartner(ctx, monetization.CreatePartnerInput{
		Name: "Del " + uniqueKey("p"), ContactName: "J", ContactEmail: "j@x.test",
		ContactPhone: "+250780000001", Status: "active",
	})
	require.NoError(t, err)

	a, err := repo.CreateAdvert(ctx, monetization.CreateAdvertInput{
		PartnerID: p.ID, Headline: "h", CtaLabel: "c", CtaLink: "https://x.test", Active: true, Priority: 0,
	})
	require.NoError(t, err)

	require.NoError(t, repo.DeletePartner(ctx, p.ID))

	// The FK is ON DELETE CASCADE (migration 066) — the advert row must be gone.
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM adverts WHERE id = $1`, a.ID).Scan(&n))
	require.Equal(t, 0, n, "advert must be cascade-deleted with its partner")
}
