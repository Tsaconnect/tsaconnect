package handlers

import (
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The approval and rejection notices are the only emails in the product with
// structure of their own — a greeting, paragraphs, an emboldened amount. Two
// things have to be true of the rendered body at once, and they pull against
// each other: the structure the handler asked for has to arrive as markup, and
// the two strings it did not write — a submitter's name and an admin's note —
// have to arrive as text no matter what they contain.
func TestPrivateSaleApprovalRendersStructureAndEscapesTheParts(t *testing.T) {
	submission := models.PrivateSaleSubmission{
		Name:   `Ada <script>alert('name')</script>`,
		Email:  "ada@example.com",
		Amount: 2500,
	}

	msg := privateSaleStatusEmail(submission, "approved", `see <script>alert('note')</script>`, "https://tsaconnectworld.com")
	body, err := msg.Body.RenderHTML(msg.Title, msg.CTAText, msg.CTAURL)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	// The regression this guards: composed as a string of literal markup, the
	// tags arrive at the recipient as visible "<br>" and "<strong>" text.
	if !strings.Contains(body, "<strong>$2500.00</strong>") {
		t.Errorf("the amount did not render as emphasis\n---\n%s", body)
	}
	if !strings.Contains(body, "<br>") {
		t.Errorf("paragraph breaks did not render\n---\n%s", body)
	}
	for _, visible := range []string{"&lt;br&gt;", "&lt;strong&gt;"} {
		if strings.Contains(body, visible) {
			t.Errorf("the handler's own markup %q reached the recipient as text\n---\n%s", visible, body)
		}
	}

	// And the parts that are somebody else's words stay words.
	if strings.Contains(body, "<script") {
		t.Errorf("an injected script tag reached the body live\n---\n%s", body)
	}
	if strings.Count(body, "&lt;script&gt;") != 2 {
		t.Errorf("expected the name and the note to both survive as escaped text\n---\n%s", body)
	}
	if !strings.Contains(body, "Ada ") {
		t.Errorf("the submitter's name was dropped\n---\n%s", body)
	}
}

func TestPrivateSaleRejectionCarriesTheAdminNote(t *testing.T) {
	submission := models.PrivateSaleSubmission{Name: "Bola", Email: "bola@example.com", Amount: 40}

	msg := privateSaleStatusEmail(submission, "rejected", "payment never arrived", "https://tsaconnectworld.com")
	body, err := msg.Body.RenderHTML(msg.Title, msg.CTAText, msg.CTAURL)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	if !strings.Contains(body, "<strong>Note from admin:</strong>") {
		t.Errorf("the note label did not render as emphasis\n---\n%s", body)
	}
	if !strings.Contains(body, "payment never arrived") {
		t.Errorf("the admin note is missing from the body\n---\n%s", body)
	}
	if msg.CTAURL != "mailto:support@tsaconnectworld.com" {
		t.Errorf("a rejected submitter must be pointed at support, got %q", msg.CTAURL)
	}
}

// A run with no note must not leave the label hanging over nothing.
func TestPrivateSaleOmitsTheNoteBlockWhenThereIsNoNote(t *testing.T) {
	submission := models.PrivateSaleSubmission{Name: "Bola", Email: "bola@example.com", Amount: 40}

	msg := privateSaleStatusEmail(submission, "approved", "", "https://tsaconnectworld.com")
	body, err := msg.Body.RenderHTML(msg.Title, msg.CTAText, msg.CTAURL)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	if strings.Contains(body, "Note from admin") {
		t.Errorf("rendered an empty admin note\n---\n%s", body)
	}
}
