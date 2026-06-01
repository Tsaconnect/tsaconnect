package services

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

const expoPushURL = "https://exp.host/--/api/v2/push/send"

// ExpoPushService sends notifications through Expo's push service. Best-effort:
// network/decoding failures are logged and never block the caller. The only
// signal it returns is the set of tokens Expo reports as dead
// (DeviceNotRegistered) so the caller can prune them.
//
// EXPO_ACCESS_TOKEN is optional — sends work unauthenticated; set it to enable
// Expo's enhanced push security. Dormant if unset.
type ExpoPushService struct {
	accessToken string
	client      *http.Client
	url         string // overridable in tests
}

func NewExpoPushService(accessToken string) *ExpoPushService {
	return &ExpoPushService{
		accessToken: accessToken,
		client:      &http.Client{Timeout: 10 * time.Second},
		url:         expoPushURL,
	}
}

type expoMessage struct {
	To        string                 `json:"to"`
	Title     string                 `json:"title"`
	Body      string                 `json:"body"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Sound     string                 `json:"sound,omitempty"`
	ChannelID string                 `json:"channelId,omitempty"`
	Priority  string                 `json:"priority,omitempty"`
}

type expoTicket struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Details struct {
		Error string `json:"error"`
	} `json:"details"`
}

type expoResponse struct {
	Data []expoTicket `json:"data"`
}

// Send pushes the same notification to every token. Tickets come back in request
// order, so a "DeviceNotRegistered" error at index i marks tokens[i] dead.
// Returns dead tokens for the caller to prune. Nil receiver / empty tokens → no-op.
func (s *ExpoPushService) Send(tokens []string, title, body string, data map[string]interface{}) (dead []string) {
	if s == nil || len(tokens) == 0 {
		return nil
	}
	msgs := make([]expoMessage, 0, len(tokens))
	for _, t := range tokens {
		msgs = append(msgs, expoMessage{
			To: t, Title: title, Body: body, Data: data,
			Sound: "default", ChannelID: "default", Priority: "high",
		})
	}
	payload, err := json.Marshal(msgs)
	if err != nil {
		log.Printf("[ExpoPush] marshal: %v", err)
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[ExpoPush] build request: %v", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.accessToken)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[ExpoPush] send: %v", err)
		return nil
	}
	defer resp.Body.Close()

	var out expoResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("[ExpoPush] decode response: %v", err)
		return nil
	}
	for i, ticket := range out.Data {
		if i >= len(tokens) {
			break
		}
		if ticket.Status == "error" && ticket.Details.Error == "DeviceNotRegistered" {
			dead = append(dead, tokens[i])
		}
	}
	return dead
}
