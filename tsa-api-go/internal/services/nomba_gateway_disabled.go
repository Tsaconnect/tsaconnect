package services

import (
	"context"
	"errors"
)

// ErrNombaGatewayDisabled is returned by the placeholder gateway when no live
// Nomba payout integration is wired. The buy flow never touches the gateway, so
// PM automation can run fully (inbound match → release) with this in place; only
// the sell-payout path fails — clearly — until the live HTTP gateway lands.
var ErrNombaGatewayDisabled = errors.New("nomba gateway not configured")

// disabledNombaGateway is a NombaGateway that always fails InitiatePayout with a
// clear error. It exists so PMAutomationService can be constructed (dormant-by-
// default) before the live HTTP gateway (a separate gated task) is implemented.
type disabledNombaGateway struct{}

// NewDisabledNombaGateway returns a NombaGateway whose InitiatePayout always
// returns ErrNombaGatewayDisabled.
func NewDisabledNombaGateway() NombaGateway { return disabledNombaGateway{} }

func (disabledNombaGateway) InitiatePayout(_ context.Context, _ PayoutRequest) (string, error) {
	return "", ErrNombaGatewayDisabled
}
