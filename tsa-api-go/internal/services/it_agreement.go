package services

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ITAgreementVersion is the CURRENT trading-agreement version. Bump it whenever
// the wording below changes materially — every user must then re-accept before
// they can trade IT again (the gate compares acceptance rows against this).
const ITAgreementVersion = "1.0"

// ITAgreement is the client-facing agreement payload. The greeting ("Hello
// <name>,") is rendered app-side from the user's profile; the backend owns the
// version + body so legal wording can change with a version bump, no app
// release for the text itself.
type ITAgreement struct {
	Version string   `json:"version"`
	Title   string   `json:"title"`
	Intro   string   `json:"intro"`
	Clauses []string `json:"clauses"`
}

// ErrITAgreementRequired is returned by the trade gate when the user has not
// accepted the current agreement version. Handlers surface it as 403 with a
// machine code so the app can show the agreement.
var ErrITAgreementRequired = errors.New("it trading agreement not accepted")

// currentITAgreement is the served text. Keep in lockstep with
// ITAgreementVersion — bumping the wording without the version means users
// won't be re-prompted.
var currentITAgreement = ITAgreement{
	Version: ITAgreementVersion,
	Title:   "User Trading Agreement",
	Intro:   `By selecting "I Agree", you acknowledge and agree to the following:`,
	Clauses: []string{
		"A dedicated virtual account may be assigned to you, subject to successful identity verification (KYC), to facilitate supported fiat transactions.",
		"You confirm that you meet the minimum legal age and are legally permitted under the laws of your jurisdiction to use TSA Connect and trade supported digital assets.",
		"You may exchange supported fiat currencies for supported digital assets, and supported digital assets for supported fiat currencies, in accordance with applicable laws and platform policies.",
		"You authorize and control each transaction you initiate. TSA Connect is designed as a non-custodial platform and does not execute transactions on your behalf without your authorization.",
		"You are responsible for complying with all applicable laws, regulations, reporting obligations, and tax requirements in your jurisdiction.",
	},
}

// CurrentITAgreement returns the agreement to display (version + text).
func CurrentITAgreement() ITAgreement { return currentITAgreement }

// HasAcceptedITAgreement reports whether the user has accepted the CURRENT
// agreement version. A prior acceptance of an older version does not count —
// that's what forces re-consent on a version bump.
func (s *ITService) HasAcceptedITAgreement(ctx context.Context, userID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.ITAgreementAcceptance{}).
		Where("user_id = ? AND version = ?", userID, ITAgreementVersion).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AcceptITAgreement records the user's acceptance of the current version.
// Idempotent: re-accepting the same version is a no-op (no duplicate row), so a
// double-tap or retry can't spam the audit trail.
func (s *ITService) AcceptITAgreement(ctx context.Context, userID uuid.UUID, ip, userAgent string) error {
	already, err := s.HasAcceptedITAgreement(ctx, userID)
	if err != nil {
		return err
	}
	if already {
		return nil
	}
	row := &models.ITAgreementAcceptance{
		UserID:     userID,
		Version:    ITAgreementVersion,
		IPAddress:  ip,
		UserAgent:  userAgent,
		AcceptedAt: time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		// A concurrent accept created the row first — treat as success.
		if isUniqueViolation(err) {
			return nil
		}
		return err
	}
	return nil
}

// RequireITAgreement is the trade gate. Returns ErrITAgreementRequired when the
// user has not accepted the current version; nil when they may trade.
func (s *ITService) RequireITAgreement(ctx context.Context, userID uuid.UUID) error {
	ok, err := s.HasAcceptedITAgreement(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrITAgreementRequired
	}
	return nil
}
