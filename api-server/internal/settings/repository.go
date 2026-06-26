package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetAll(ctx context.Context) (map[string]interface{}, error) {
	rows, err := r.db.Query(ctx, `SELECT key, value FROM platform_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]interface{})
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var val interface{}
		if err := json.Unmarshal(raw, &val); err != nil {
			result[key] = string(raw)
		} else {
			result[key] = val
		}
	}
	return result, nil
}

func (r *Repository) Get(ctx context.Context, key string) (interface{}, error) {
	var raw []byte
	err := r.db.QueryRow(ctx, `SELECT value FROM platform_settings WHERE key = $1`, key).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var val interface{}
	if err := json.Unmarshal(raw, &val); err != nil {
		return string(raw), nil
	}
	return val, nil
}

func (r *Repository) Set(ctx context.Context, key string, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO platform_settings (key, value, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
	`, key, raw, time.Now())
	return err
}

func (r *Repository) UpdateRegion(ctx context.Context, regionID string, updates map[string]interface{}) error {
	var rawVal []byte
	if err := r.db.QueryRow(ctx, `SELECT value FROM platform_settings WHERE key = 'regions'`).Scan(&rawVal); err != nil {
		return err
	}
	var regions []map[string]interface{}
	if err := json.Unmarshal(rawVal, &regions); err != nil {
		return err
	}
	for i, region := range regions {
		if id, ok := region["id"].(string); ok && id == regionID {
			for k, v := range updates {
				regions[i][k] = v
			}
			break
		}
	}
	newRaw, err := json.Marshal(regions)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `UPDATE platform_settings SET value=$1, updated_at=NOW() WHERE key='regions'`, newRaw)
	return err
}

func (r *Repository) CreateRegion(ctx context.Context, name, status string) (map[string]interface{}, error) {
	var rawVal []byte
	if err := r.db.QueryRow(ctx, `SELECT value FROM platform_settings WHERE key = 'regions'`).Scan(&rawVal); err != nil {
		rawVal = []byte("[]")
	}
	var regions []map[string]interface{}
	_ = json.Unmarshal(rawVal, &regions)

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	newRegion := map[string]interface{}{
		"id": id, "name": name, "status": status, "drivers": 0,
	}
	regions = append(regions, newRegion)

	newRaw, err := json.Marshal(regions)
	if err != nil {
		return nil, err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO platform_settings (key, value, updated_at)
		VALUES ('regions', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
	`, newRaw)
	if err != nil {
		return nil, err
	}
	return newRegion, nil
}

func (r *Repository) DeleteRegion(ctx context.Context, regionID string) error {
	var rawVal []byte
	if err := r.db.QueryRow(ctx, `SELECT value FROM platform_settings WHERE key = 'regions'`).Scan(&rawVal); err != nil {
		return err
	}
	var regions []map[string]interface{}
	if err := json.Unmarshal(rawVal, &regions); err != nil {
		return err
	}
	filtered := regions[:0]
	for _, region := range regions {
		if id, ok := region["id"].(string); !ok || id != regionID {
			filtered = append(filtered, region)
		}
	}
	newRaw, err := json.Marshal(filtered)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `UPDATE platform_settings SET value=$1, updated_at=NOW() WHERE key='regions'`, newRaw)
	return err
}
