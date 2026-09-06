package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"draw/internal/model"
)

const defaultBaselineQuality = 0.3

type SQLiteSourceRegistry struct {
	db              *sql.DB
	baselineQuality float64
}

func NewSQLiteSourceRegistry(db *sql.DB, baselineQuality float64) (*SQLiteSourceRegistry, error) {
	if baselineQuality == 0 {
		baselineQuality = defaultBaselineQuality
	}
	return &SQLiteSourceRegistry{
		db:              db,
		baselineQuality: baselineQuality,
	}, nil
}

func (r *SQLiteSourceRegistry) Lookup(domain string) (*model.SourceProfile, bool) {
	ctx := context.Background()
	domain = strings.ToLower(domain)

	const q = `SELECT domain, class, quality_score, crawl_depth_limit, per_domain_limit, rate_limit_rpm, auth_type, last_observed_at, provisional, denied FROM source_profiles WHERE domain = ?`

	var p model.SourceProfile
	var className, authName string
	var lastObserved sql.NullTime
	var provisionalInt, deniedInt int

	err := r.db.QueryRowContext(ctx, q, domain).Scan(
		&p.Domain, &className, &p.QualityScore, &p.CrawlDepthLimit,
		&p.PerDomainLimit, &p.RateLimitRPM, &authName, &lastObserved,
		&provisionalInt, &deniedInt,
	)

	if err == sql.ErrNoRows {
		return &model.SourceProfile{
			Domain:          domain,
			Class:           model.SourceClassUnknown,
			QualityScore:    r.baselineQuality,
			CrawlDepthLimit: 3,
			PerDomainLimit:  5,
			RateLimitRPM:    60,
			AuthType:        model.AuthTypeNone,
			LastObservedAt:  time.Time{},
			Provisional:     true,
			Denied:          false,
		}, true
	}
	if err != nil {
		return nil, false
	}

	p.Domain = strings.ToLower(p.Domain)
	p.Class = model.SourceClass(className)
	p.AuthType = model.AuthType(authName)
	p.Provisional = provisionalInt != 0
	p.Denied = deniedInt != 0

	if lastObserved.Valid {
		p.LastObservedAt = lastObserved.Time
	}

	if p.Denied {
		// Deny-listed profiles are returned (ok=true) so downstream callers —
		// the frontier hard-filter (isDeniedDomain) and the fetch-boundary
		// denylist (ShouldDeny) — can observe p.Denied and REJECT the domain.
		// Returning (nil,false) would make an explicit deny indistinguishable
		// from an unknown domain, silently bypassing H29/Cor.5.
		return &p, true
	}

	return &p, true
}

func (r *SQLiteSourceRegistry) Upsert(p model.SourceProfile) error {
	ctx := context.Background()
	domain := strings.ToLower(p.Domain)

	const q = `INSERT INTO source_profiles 
		(domain, class, quality_score, crawl_depth_limit, per_domain_limit, rate_limit_rpm, auth_type, last_observed_at, provisional, denied)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
	ON CONFLICT(domain) DO UPDATE SET
		class = excluded.class,
		quality_score = excluded.quality_score,
		crawl_depth_limit = excluded.crawl_depth_limit,
		per_domain_limit = excluded.per_domain_limit,
		rate_limit_rpm = excluded.rate_limit_rpm,
		auth_type = excluded.auth_type,
		last_observed_at = excluded.last_observed_at,
		provisional = 0,
		denied = 0`

	_, err := r.db.ExecContext(ctx, q,
		domain, string(p.Class), p.QualityScore, p.CrawlDepthLimit,
		p.PerDomainLimit, p.RateLimitRPM, string(p.AuthType), p.LastObservedAt,
	)
	if err != nil {
		return fmt.Errorf("source registry upsert: %w", err)
	}
	return nil
}

func (r *SQLiteSourceRegistry) ProvisionalUpsert(p model.SourceProfile) error {
	ctx := context.Background()
	domain := strings.ToLower(p.Domain)

	const q = `INSERT INTO source_profiles 
		(domain, class, quality_score, crawl_depth_limit, per_domain_limit, rate_limit_rpm, auth_type, last_observed_at, provisional, denied)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0)
	ON CONFLICT(domain) DO UPDATE SET
		class = excluded.class,
		quality_score = CASE 
			WHEN source_profiles.denied = 1 THEN source_profiles.quality_score
			WHEN source_profiles.provisional = 0 THEN source_profiles.quality_score
			ELSE excluded.quality_score
		END,
		crawl_depth_limit = CASE
			WHEN source_profiles.provisional = 0 THEN source_profiles.crawl_depth_limit
			ELSE excluded.crawl_depth_limit
		END,
		per_domain_limit = excluded.per_domain_limit,
		rate_limit_rpm = excluded.rate_limit_rpm,
		auth_type = excluded.auth_type,
		last_observed_at = excluded.last_observed_at,
		provisional = 1,
		denied = source_profiles.denied`

	_, err := r.db.ExecContext(ctx, q,
		domain, string(p.Class), p.QualityScore, p.CrawlDepthLimit,
		p.PerDomainLimit, p.RateLimitRPM, string(p.AuthType), p.LastObservedAt,
	)
	if err != nil {
		return fmt.Errorf("source registry provisional upsert: %w", err)
	}
	return nil
}

func (r *SQLiteSourceRegistry) Deny(domain string) error {
	ctx := context.Background()
	domain = strings.ToLower(domain)

	const q = `INSERT INTO source_profiles 
		(domain, class, quality_score, crawl_depth_limit, per_domain_limit, rate_limit_rpm, auth_type, last_observed_at, provisional, denied)
	VALUES (?, 'UNKNOWN', 0.0, 3, 5, 60, 'NONE', NULL, 0, 1)
	ON CONFLICT(domain) DO UPDATE SET denied = 1, provisional = 0`

	_, err := r.db.ExecContext(ctx, q, domain)
	if err != nil {
		return fmt.Errorf("source registry deny: %w", err)
	}
	return nil
}
